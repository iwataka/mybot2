package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"sync"

	"github.com/sivchari/gotwtr"
	"github.com/slack-go/slack"
	"gopkg.in/Knetic/govaluate.v2"
	"gopkg.in/alecthomas/kingpin.v2"
	"gopkg.in/yaml.v3"
)

const (
	twitterType = "Twitter"
	slackType   = "Slack"
)

const (
	twitterURLTemplate = "https://twitter.com/%s/status/%s"
)

var (
	twitterToken = kingpin.Flag("twitter-token", "Twitter bearer token").Envar("MYBOT_TWITTER_TOKEN").Required().String()
	slackToken   = kingpin.Flag("slack-token", "Slack bearer token").Envar("MYBOT_SLACK_TOKEN").Required().String()
	configFile = kingpin.Flag("config", "Config file").Default("config.yaml").File()
)

func main() {
	kingpin.Parse()

	ctx := Context{
		context:       context.Background(),
		twitterClient: gotwtr.New(*twitterToken),
		slackClient:   slack.New(*slackToken),
	}

	bs, err := ioutil.ReadAll(*configFile)
	if err != nil {
		panic(err)
	}
	config := &Config{}
	err = yaml.Unmarshal(bs, config)
	if err != nil {
		panic(err)
	}

	outChan := make(chan SourceData)
	errChan := make(chan error)
	wg := sync.WaitGroup{}
	config.Source.Start(ctx, outChan, errChan, wg)

	go func() {
		for {
			select {
			case out, ok := <-outChan:
				if !ok {
					return
				}
				for _, proc := range config.Process {
					match, err := proc.Filter.Match(out)
					if err != nil {
						errChan <- err
					}
					if match {
						proc.Action.Do(ctx, out)
					}
				}
			case err, ok := <-errChan:
				if !ok {
					return
				}
				fmt.Printf("%v\n", err)
			case <-ctx.context.Done():
				return
			}
		}
	}()

	err = <-errChan
	ctx.context.Done()
	fmt.Fprintln(os.Stderr, err)
}

type Context struct {
	context       context.Context
	twitterClient *gotwtr.Client
	slackClient   *slack.Client
}

type Config struct {
	Source  Source    `yaml:"source"`
	Process []Process `yaml:"process"`
}

type Process struct {
	Filter Filter `yaml:"filter"`
	Action Action `yaml:"action"`
}

type Source struct {
	Twitter TwitterSource `yaml:"twitter"`
	Slack   SlackSource   `yaml:"slack"`
}

func (s *Source) Start(ctx Context, outChan chan<- SourceData, errChan chan<- error, wg sync.WaitGroup) {
	s.Twitter.Start(ctx, outChan, errChan, wg)
	s.Slack.Start(ctx, outChan, errChan, wg)
}

type ISource interface {
	Start(ctx Context, outChan chan<- SourceData, errChan chan<- error, wg sync.WaitGroup)
}

type TwitterSource struct {
	FilteredStream TwitterFilteredStreamSource `yaml:"filtered_stream"`
}

func (s *TwitterSource) Start(ctx Context, outChan chan<- SourceData, errChan chan<- error, wg sync.WaitGroup) {
	s.FilteredStream.Start(ctx, outChan, errChan, wg)
}

type TwitterFilteredStreamSource struct {
	Enabled bool `yaml:"enabled"`
}

func (s *TwitterFilteredStreamSource) Start(ctx Context, outChan chan<- SourceData, errChan chan<- error, wg sync.WaitGroup) {
	if !s.Enabled {
		return
	}

	ch := make(chan gotwtr.ConnectToStreamResponse)
	stream := ctx.twitterClient.ConnectToStream(ctx.context, ch, errChan)
	wg.Add(1)
	go func() {
		defer func() {
			stream.Stop()
			wg.Done()
		}()
		for {
			select {
			case resp, ok := <-ch:
				if !ok {
					return
				}
				links := []string{}
				for _, url := range resp.Tweet.Entities.URLs {
					links = append(links, url.ExpandedURL)
				}
				outChan <- SourceData{
					Type:  twitterType,
					ID:    resp.Tweet.ID,
					Text:  resp.Tweet.Text,
					URL:   fmt.Sprintf(twitterURLTemplate, resp.Tweet.AuthorID, resp.Tweet.ID),
					Links: links,
				}
			case <-ctx.context.Done():
				return
			}
		}
	}()
}

type SlackSource struct {
	RTMs []SlackRTMSource `yaml:"rtms"`
}

func (s *SlackSource) Start(ctx Context, outChan chan<- SourceData, errChan chan<- error, wg sync.WaitGroup) {
	for _, rtm := range s.RTMs {
		rtm.Start(ctx, outChan, errChan, wg)
	}
}

type SlackRTMSource struct{}

func (s *SlackRTMSource) Start(ctx Context, outChan chan<- SourceData, errChan chan<- error, wg sync.WaitGroup) {
	// TODO: implement this
	panic("not implemented")
}

type SourceData struct {
	Type   string   `yaml:"type"`
	ID     string   `yaml:"id"`
	Text   string   `yaml:"text"`
	URL    string   `yaml:"url"`
	Links  []string `yaml:"links"`
	Images []string `yaml:"images"`
}

type Filter struct {
	Condition string `yaml:"condition"`
}

var expressionFunctions = map[string]govaluate.ExpressionFunction{}

func (f *Filter) Match(data SourceData) (bool, error) {
	exp, err := govaluate.NewEvaluableExpressionWithFunctions(f.Condition, expressionFunctions)
	if err != nil {
		return false, err
	}
	params := map[string]interface{}{
		"data": data,
	}
	result, err := exp.Evaluate(params)
	if err != nil {
		return false, err
	}
	return result.(bool), nil
}

func doAction(action IAction, ctx Context, data SourceData) error {
	if action != nil {
		return action.Do(ctx, data)
	}
	return nil
}

func doActions(actions []IAction, ctx Context, data SourceData) error {
	for _, action := range actions {
		err := action.Do(ctx, data)
		if err != nil {
			return err
		}
	}
	return nil
}

type IAction interface {
	Do(ctx Context, data SourceData) error
}

type Action struct {
	Twitter *TwitterAction `yaml:"twitter"`
	Slack   *SlackAction   `yaml:"slack"`
}

func (a *Action) Do(ctx Context, data SourceData) error {
	err := doActions([]IAction{a.Twitter, a.Slack}, ctx, data)
	if err != nil {
		return err
	}
	return nil
}

type TwitterAction struct {
	Tweet   *TwitterTweetAction   `yaml:"tweet"`
	Retweet *TwitterRetweetAction `yaml:"retweet"`
	Like    *TwitterLikeAction    `yaml:"like"`
}

func (a *TwitterAction) Do(ctx Context, data SourceData) error {
	err := doActions([]IAction{a.Tweet, a.Retweet, a.Like}, ctx, data)
	if err != nil {
		return err
	}
	return nil
}

type TwitterTweetAction struct {
	Enabled bool `yaml:"enabled"`
}

func (a *TwitterTweetAction) Do(ctx Context, data SourceData) error {
	if !a.Enabled {
		return nil
	}

	body := &gotwtr.PostTweetOption{
		Text: data.Text,
	}
	_, err := ctx.twitterClient.PostTweet(ctx.context, body)
	if err != nil {
		return err
	}

	return nil
}

type TwitterRetweetAction struct {
	Enabled bool `yaml:"enabled"`
}

func (a *TwitterRetweetAction) Do(ctx Context, data SourceData) error {
	if !a.Enabled {
		return nil
	}

	userResp, err := ctx.twitterClient.RetrieveSingleUserWithUserName(ctx.context, "me")
	if err != nil {
		return err
	}
	userID := userResp.User.ID

	_, err = ctx.twitterClient.PostRetweet(ctx.context, userID, data.ID)
	if err != nil {
		return err
	}

	return nil
}

type TwitterLikeAction struct {
	Enabled bool `yaml:"enabled"`
}

func (a *TwitterLikeAction) Do(ctx Context, data SourceData) error {
	if !a.Enabled {
		return nil
	}

	userResp, err := ctx.twitterClient.RetrieveSingleUserWithUserName(ctx.context, "me")
	if err != nil {
		return err
	}
	userID := userResp.User.ID

	_, err = ctx.twitterClient.PostUsersLikingTweet(ctx.context, userID, data.ID)
	if err != nil {
		return err
	}

	return nil
}

type SlackAction struct {
	Message  *SlackMessageAction  `yaml:"message"`
	Pin      *SlackPinAction      `yaml:"pin"`
	Reaction *SlackReactionAction `yaml:"reaction"`
}

func (a *SlackAction) Do(ctx Context, data SourceData) error {
	err := doActions([]IAction{a.Message, a.Pin, a.Reaction}, ctx, data)
	if err != nil {
		return err
	}
	return nil
}

type SlackMessageAction struct {
	Channels []string `yaml:"channels"`
}

func (a *SlackMessageAction) Do(ctx Context, data SourceData) error {
	for _, ch := range a.Channels {
		_, _, err := ctx.slackClient.PostMessage(ch, slack.MsgOptionText(data.URL, false))
		if err != nil {
			return err
		}
	}
	return nil
}

type SlackPinAction struct {
	Enabled bool `yaml:"enabled"`
}

func (a *SlackPinAction) Do(ctx Context, data SourceData) error {
	if !a.Enabled {
		return nil
	}

	// TODO: implement this
	panic("not implemented")
}

type SlackReactionAction struct {
	Reactions []string `yaml:"reactions"`
}

func (a *SlackReactionAction) Do(ctx Context, data SourceData) error {
	// TODO: implement this
	panic("not implemented")
}

// TODO: HTTP action (which sends requests to specific URLs) and source (which receives HTTP responses)
