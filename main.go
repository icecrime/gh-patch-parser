package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/bitly/go-nsq"
	"github.com/crosbymichael/octokat"
	"github.com/drone/go-github/github"
)

const (
	VERSION = "v0.1.0"
)

var (
	lookupd string
	topic   string
	channel string
	ghtoken string
	debug   bool
	version bool
)

func init() {
	// parse flags
	flag.BoolVar(&version, "version", false, "print version and exit")
	flag.BoolVar(&version, "v", false, "print version and exit (shorthand)")
	flag.BoolVar(&debug, "d", false, "run in debug mode")
	flag.StringVar(&lookupd, "lookupd-addr", "nsqlookupd:4161", "nsq lookupd address")
	flag.StringVar(&topic, "topic", "hooks-docker", "nsq topic")
	flag.StringVar(&channel, "channel", "patch-parser", "nsq channel")
	flag.StringVar(&ghtoken, "gh-token", "", "github access token")
	flag.Parse()
}

type Handler struct {
	GHToken string
}

func (h *Handler) HandleMessage(m *nsq.Message) error {
	prHook, err := github.ParsePullRequestHook(m.Body)
	if err != nil {
		// Errors will most likely occur because not all GH
		// hooks are the same format
		// we care about those that are a new pull request
		log.Debugf("Error parsing hook: %v", err)
		return nil
	}

	// we only want opened pull requests
	if !prHook.IsOpened() {
		return nil
	}

	pr, err := getPR(prHook.PullRequest.Url)
	if err != nil {
		return err
	}

	var labels []string

	// check if it's a proposal
	isProposal := strings.Contains(strings.ToLower(prHook.PullRequest.Title), "proposal")
	switch {
	case isProposal:
		labels = []string{"1-design-review"}
	case isDocsOnly(pr):
		labels = []string{"3-docs-review"}
	default:
		labels = []string{"0-triage"}
	}

	// sleep before we apply the labels to try and stop waffle from removing them
	// this is gross i know
	time.Sleep(30 * time.Second)

	// initialize github client
	gh := octokat.NewClient()
	gh = gh.WithToken(h.GHToken)
	repo := octokat.Repo{
		Name:     prHook.PullRequest.Base.Repo.Name,
		UserName: prHook.PullRequest.Base.Repo.Owner.Login,
	}

	// add labels if there are any
	if len(labels) > 0 {

		prIssue := octokat.Issue{
			Number: prHook.Number,
		}
		log.Debugf("Adding labels %#v to pr %d", labels, prHook.Number)
		if err := gh.ApplyLabel(repo, &prIssue, labels); err != nil {
			return err
		}

		log.Infof("Added labels %#v to pr %d", labels, prHook.Number)
	}

	// check if all the commits are signed
	if !commitsAreSigned(pr) {
		// add comment about having to sign commits
		comment := `Can you please sign your commits following these rules:

https://github.com/docker/docker/blob/master/CONTRIBUTING.md#sign-your-work

The easiest way to do this is to amend the last commit:

~~~console
`
		comment += fmt.Sprintf("$ git clone -b %q %s %s\n", pr.Head.Ref, pr.Head.Repo.SSHURL, "somewhere")
		comment += fmt.Sprintf("$ cd %s\n", "somewhere")
		if pr.Commits > 1 {
			comment += fmt.Sprintf("$ git rebase -i HEAD~%d\n", pr.Commits)
			comment += "editor opens\nchange each 'pick' to 'edit'\nsave the file and quit\n"
		}
		comment += "$ git commit --amend -s --no-edit\n"
		if pr.Commits > 1 {
			comment += "$ git rebase --continue # and repeat the amend for each commit\n"
		}
		comment += "$ git push -f\n"
		comment += `~~~`

		if _, err := gh.AddComment(repo, strconv.Itoa(prHook.Number), comment); err != nil {
			return err
		}
		log.Infof("Added comment to unsigned PR %d", prHook.Number)
	}

	// checkout the repository in a temp dir
	temp, err := ioutil.TempDir("", fmt.Sprintf("pr-%d", prHook.Number))
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)

	if err := checkout(temp, pr.Base.Repo.HTMLURL, prHook.Number); err != nil {
		// if it is a merge error, comment on the PR
		if err == MergeError {
			comment := "Looks like we would not be able to merge this PR because of conflicts. Please fix them and force push to your branch."

			if _, err := gh.AddComment(repo, strconv.Itoa(prHook.Number), comment); err != nil {
				return err
			}
			log.Infof("Added comment to unmergable PR %d", prHook.Number)
			return nil
		}
		return err
	}

	// check if the files are gofmt'd
	isGoFmtd, files := checkGofmt(temp, pr)
	if !isGoFmtd {
		comment := fmt.Sprintf("These files are not properly gofmt'd:\n%s\n", strings.Join(files, "\n"))
		comment += "Please reformat the above files using `gofmt -s -w` and ammend to the commit the result."

		if _, err := gh.AddComment(repo, strconv.Itoa(prHook.Number), comment); err != nil {
			return err
		}
		log.Infof("Added comment to non-gofmt'd PR %d", prHook.Number)
	}

	return nil
}

func main() {
	// set log level
	if debug {
		log.SetLevel(log.DebugLevel)
	}

	if version {
		fmt.Println(VERSION)
		return
	}

	bb := &Handler{GHToken: ghtoken}
	if err := ProcessQueue(bb, QueueOptsFromContext(topic, channel, lookupd)); err != nil {
		log.Fatal(err)
	}
}
