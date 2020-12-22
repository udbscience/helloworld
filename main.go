package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/bitly/go-nsq"
	"github.com/crosbymichael/octokat"
	"github.com/sirupsen/logrus"
)

// VERSION of code
const (
	VERSION = "v0.1.0"
)

var (
	lookupd string
	topic   string
	channel string
	debug   bool
	version bool
)

// Handler is the message processing interface for the consumer to nsq.
type Handler struct{}

// HandleMessage reads the nsq message body and parses it as a github webhook,
// checks out the source for the repository & executes the given script in the source tree.
func (h *Handler) HandleMessage(m *nsq.Message) error {
	logrus.Debugf("parsing hook: %v", string(m.Body))
	script := "gcloud builds submit . --async"
	hook, err := octokat.ParseHook(m.Body)
	if err != nil {
		// Errors will most likely occur because not all GH
		// hooks are the same format
		// we care about those that are pushes to master
		logrus.Debugf("Error parsing hook: %v", err)
		return nil
	}

	// we only care about pushes to master
	// if hook.Branch() != "master" {
	// 	return nil
	// }
	shortSha := hook.After[0:7]
	workerPool := "--worker-pool=projects/mlb-xpn-shared-9d4c/locations/us-central1/workerPools/builder-pool"
	gcsLogDir := "--gcs-log-dir=gs://mlb-xpn-shared-9d4c_cloudbuild/builder"
	tagName := ""
	substitutions := "--substitutions="
	substitutions = substitutions + fmt.Sprintf("REPO_NAME=%s,", hook.Repo.Name)
	substitutions = substitutions + fmt.Sprintf("BRANCH_NAME=%s,", hook.Branch())
	substitutions = substitutions + fmt.Sprintf("COMMIT_SHA=%s,", hook.After)
	substitutions = substitutions + fmt.Sprintf("SHORT_SHA=%s,", shortSha)
	if hook.IsTag() {
		tagName = hook.Ref
	}
	substitutions = substitutions + fmt.Sprintf("TAG_NAME=%s", tagName)

	temp, err := ioutil.TempDir("", fmt.Sprintf("nsqexec-commit-%s", shortSha))
	if err != nil {
		return err
	}
	//defer os.RemoveAll(temp)

	if err := checkout(temp, hook.Repo.URL, hook.After, hook.Branch()); err != nil {
		return err
	}
	logrus.Debugf("Checked out %s for %s", hook.After, hook.Repo.URL)

	// execute the script
	cmd := exec.Command("echo", script, workerPool, gcsLogDir, substitutions)
	cmd.Dir = temp
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("Running script %s failed with output: %s\nerror: %v", script, string(out), err)
	}

	logrus.Debugf("Output of %s: %s", script, string(out))

	return nil
}

// QueueOpts are the options for the nsq queue.
type QueueOpts struct {
	LookupdAddr string
	Topic       string
	Channel     string
	Concurrent  int
	Signals     []os.Signal
}

// QueueOptsFromContext returns a QueueOpts object from the given settings.
func QueueOptsFromContext(topic, channel, lookupd string) QueueOpts {
	return QueueOpts{
		Signals:     []os.Signal{syscall.SIGTERM, syscall.SIGINT},
		LookupdAddr: lookupd,
		Topic:       topic,
		Channel:     channel,
		Concurrent:  1,
	}
}

// ProcessQueue sets up the handler to process the nsq queue with the given options.
func ProcessQueue(handler nsq.Handler, opts QueueOpts) error {
	if opts.Concurrent == 0 {
		opts.Concurrent = 1
	}
	s := make(chan os.Signal, 64)
	signal.Notify(s, opts.Signals...)

	consumer, err := nsq.NewConsumer(opts.Topic, opts.Channel, nsq.NewConfig())
	if err != nil {
		return err
	}
	consumer.AddConcurrentHandlers(handler, opts.Concurrent)
	if err := consumer.ConnectToNSQLookupd(opts.LookupdAddr); err != nil {
		return err
	}

	for {
		select {
		case <-consumer.StopChan:
			return nil
		case sig := <-s:
			logrus.WithField("signal", sig).Debug("received signal")
			consumer.Stop()
		}
	}
}

// checkout `git clones` a repo
func checkout(temp, repo, sha, branch string) error {
	// don't clone the whole repo, it's too slow
	cmd := exec.Command("git", "clone", "--depth=100", "--recursive", "--branch="+branch, repo, temp)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Running command failed: %s, %v", string(output), err)
	}

	// checkout a commit (or branch or tag) of interest
	cmd = exec.Command("git", "checkout", "-qf", sha)
	cmd.Dir = temp
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Running command failed: %s, %v", string(output), err)
	}

	return nil
}

func init() {
	// parse flags
	flag.BoolVar(&version, "version", false, "print version and exit")
	flag.BoolVar(&version, "v", false, "print version and exit (shorthand)")
	flag.BoolVar(&debug, "d", false, "run in debug mode")
	flag.StringVar(&lookupd, "lookupd-addr", "nsqlookupd:4161", "nsq lookupd address")
	flag.StringVar(&topic, "topic", "hooks-docker", "nsq topic")
	flag.StringVar(&channel, "channel", "exec-hook", "nsq channel")
	flag.Parse()
}

func main() {
	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	}
	if version {
		fmt.Println(VERSION)
		return
	}
	bb := &Handler{}
	if err := ProcessQueue(bb, QueueOptsFromContext(topic, channel, lookupd)); err != nil {
		logrus.Fatal(err)
	}
}
