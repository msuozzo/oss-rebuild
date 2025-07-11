// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/google/oss-rebuild/internal/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/diffoscope"
	"github.com/google/oss-rebuild/tools/ctl/ide/assistant"
	"github.com/google/oss-rebuild/tools/ctl/ide/chatbox"
	"github.com/google/oss-rebuild/tools/ctl/ide/choice"
	"github.com/google/oss-rebuild/tools/ctl/ide/details"
	"github.com/google/oss-rebuild/tools/ctl/ide/modal"
	"github.com/google/oss-rebuild/tools/ctl/ide/rebuilder"
	"github.com/google/oss-rebuild/tools/ctl/ide/textinput"
	"github.com/google/oss-rebuild/tools/ctl/ide/tmux"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

const (
	RundexReadParallelism = 10
	LLMRequestParallelism = 50
	expertPrompt          = `You are an expert in diagnosing build issues in multiple open source ecosystems. You will help diagnose why builds failed, or why the builds might have produced an artifact that differs from the upstream open source package. Provide clear and concise explantions of why the rebuild failed, and suggest changes that could fix the rebuild`
)

// A modalFnType can be used to show an InputCaptureable. It returns an exit function that can be used to close the modal.
type modalFnType func(modal.InputCaptureable, modal.ModalOpts) func()

func NewRebuildCmds(app *tview.Application, rb *rebuilder.Rebuilder, modalFn modalFnType, butler localfiles.Butler, aiClient *genai.Client, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository) []RebuildCmd {
	return []RebuildCmd{
		{
			Short: "run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{})
			},
		},
		{
			Short: "restart && run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				rb.Restart(ctx)
				rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{})
			},
		},
		{
			Short: "edit and run local",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				buildDefAsset := rebuild.BuildDef.For(example.Target())
				var currentStrat schema.StrategyOneOf
				{
					if r, err := buildDefs.Reader(ctx, buildDefAsset); err == nil {
						d := yaml.NewDecoder(r)
						if d.Decode(&currentStrat) != nil {
							log.Println(errors.Wrap(err, "failed to read existing build definition"))
							return
						}
					} else {
						currentStrat = example.Strategy
						s, err := currentStrat.Strategy()
						if err != nil {
							log.Println(errors.Wrap(err, "unpacking StrategyOneOf"))
							return
						}
						// Convert this strategy to a workflow strategy if possible.
						if fable, ok := s.(rebuild.Flowable); ok {
							currentStrat = schema.NewStrategyOneOf(fable.ToWorkflow())
						}
					}
				}
				var newStrat schema.StrategyOneOf
				{
					w, err := buildDefs.Writer(ctx, buildDefAsset)
					if err != nil {
						log.Println(errors.Wrapf(err, "opening build definition"))
						return
					}
					if _, err = w.Write([]byte("# Edit the build definition below, then save and exit the file to begin a rebuild.\n")); err != nil {
						log.Println(errors.Wrapf(err, "writing comment to build definition file"))
						return
					}
					enc := yaml.NewEncoder(w)
					if enc.Encode(&currentStrat) != nil {
						log.Println(errors.Wrapf(err, "populating build definition"))
						return
					}
					w.Close()
					editor := os.Getenv("EDITOR")
					if editor == "" {
						editor = "vim"
					}
					if err := tmux.Wait(fmt.Sprintf("%s %s", editor, buildDefs.URL(buildDefAsset).Path)); err != nil {
						log.Println(errors.Wrap(err, "editing build definition"))
						return
					}
					r, err := buildDefs.Reader(ctx, buildDefAsset)
					if err != nil {
						log.Println(errors.Wrap(err, "failed to open build definition after edits"))
						return
					}
					d := yaml.NewDecoder(r)
					if err := d.Decode(&newStrat); err != nil {
						log.Println(errors.Wrap(err, "manual strategy oneof failed to parse"))
						return
					}
				}
				rb.RunLocal(ctx, example, rebuilder.RunLocalOpts{Strategy: &newStrat})
			},
		},
		{
			Hotkey: 'm',
			Short:  "metadata",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				if deets, err := details.View(example); err != nil {
					log.Println(err.Error())
					return
				} else {
					modalFn(deets, modal.ModalOpts{Margin: 10})
				}
			},
		},
		{
			Hotkey: 'l',
			Short:  "logs",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				if example.Artifact == "" {
					log.Println("Rundex does not have the artifact, cannot find GCS path.")
					return
				}
				logs, err := butler.Fetch(ctx, example.RunID, example.WasSmoketest(), rebuild.DebugLogsAsset.For(example.Target()))
				if err != nil {
					log.Println(errors.Wrap(err, "downloading logs"))
					return
				}
				if err := tmux.Start(fmt.Sprintf("cat %s | less", logs)); err != nil {
					log.Println(errors.Wrap(err, "failed to read logs"))
				}
			},
		},
		{
			Hotkey: 'd',
			Short:  "diff",
			Func: func(ctx context.Context, example rundex.Rebuild) {
				path, err := butler.Fetch(ctx, example.RunID, example.WasSmoketest(), diffoscope.DiffAsset.For(example.Target()))
				if err != nil {
					log.Println(errors.Wrap(err, "fetching diff"))
					return
				}
				if err := tmux.Wait(fmt.Sprintf("less -R %s", path)); err != nil {
					log.Println(errors.Wrap(err, "running diffoscope"))
					return
				}
			},
		},
		{
			Short: "debug with ✨AI✨",
			DisabledMsg: func() string {
				if aiClient == nil {
					return "To enable AI features, provide a gcloud project with Vertex AI API enabled."
				}
				return ""
			},
			Func: func(ctx context.Context, example rundex.Rebuild) {
				var config *genai.GenerateContentConfig
				{
					config = &genai.GenerateContentConfig{
						Temperature:     genai.Ptr(float32(0.1)),
						MaxOutputTokens: int32(16000),
					}
					systemPrompt := []*genai.Part{
						{Text: expertPrompt},
					}
					config = llm.WithSystemPrompt(config, systemPrompt...)
				}
				s, err := assistant.NewAssistant(butler, aiClient, llm.GeminiFlash, config).Session(ctx, example)
				if err != nil {
					log.Println(errors.Wrap(err, "creating session"))
					return
				}
				cb := chatbox.NewChatbox(app, s, chatbox.ChatBoxOpts{Welcome: "Debug with AI! Type /help for a list of commands.", InputHeader: "Ask the AI"})
				modalExit := modalFn(cb.Widget(), modal.ModalOpts{Margin: 10})
				go cb.HandleInput(ctx, "/debug")
				go func() {
					<-cb.Done()
					modalExit()
				}()
			},
		},
	}
}

func NewRebuildGroupCmds(app *tview.Application, rb *rebuilder.Rebuilder, modalFn modalFnType, butler localfiles.Butler, aiClient *genai.Client, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository) []RebuildGroupCmd {
	return []RebuildGroupCmd{
		{
			Short: "Find pattern",
			Func: func(ctx context.Context, rebuilds []rundex.Rebuild) {
				pattern, mopts, inputChan := textinput.TextInput(textinput.TextInputOpts{Header: "Search Regex"})
				exitFunc := modalFn(pattern, mopts)
				input := <-inputChan
				log.Printf("Finding pattern \"%s\"", input)
				exitFunc()
				regex, err := regexp.Compile(input)
				if err != nil {
					log.Println(err.Error())
					return
				}
				p := pipe.FromSlice(rebuilds)
				p = p.ParDo(RundexReadParallelism, func(in rundex.Rebuild, out chan<- rundex.Rebuild) {
					_, err := butler.Fetch(context.Background(), in.RunID, in.WasSmoketest(), rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrap(err, "downloading logs"))
						return
					}
					out <- in
				})
				var found int
				p = p.Do(func(in rundex.Rebuild, out chan<- rundex.Rebuild) {
					assets, err := localfiles.AssetStore(in.RunID)
					if err != nil {
						log.Println(errors.Wrapf(err, "creating asset store for runid: %s", in.RunID))
						return
					}
					r, err := assets.Reader(ctx, rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrapf(err, "opening logs for %s", in.ID()))
						return
					}
					defer r.Close()
					// TODO: Maybe read the whole file into memory and do multi-line matching?
					scanner := bufio.NewScanner(r)
					for scanner.Scan() {
						line := scanner.Text()
						if regex.MatchString(line) {
							log.Printf("%s\n\t%s", in.ID(), line)
							out <- in
							break
						}
					}
					if err := scanner.Err(); err != nil {
						log.Println(errors.Wrap(err, "reading logs"))
					}
				})
				for range p.Out() {
				}
				log.Printf("Found in %d/%d (%2.0f%%)", found, len(rebuilds), float32(found)/float32(len(rebuilds))*100)
			},
		},
		{
			Short: "Cluster using AI",
			DisabledMsg: func() string {
				if aiClient == nil {
					return "To enable AI features, provide a gcloud project with Vertex AI API enabled."
				}
				return ""
			},
			Func: func(ctx context.Context, rebuilds []rundex.Rebuild) {
				var config *genai.GenerateContentConfig
				{
					config = &genai.GenerateContentConfig{
						Temperature:     genai.Ptr(float32(0.1)),
						MaxOutputTokens: int32(16000),
					}
					systemPrompt := []*genai.Part{
						{Text: expertPrompt},
					}
					config = llm.WithSystemPrompt(config, systemPrompt...)
				}
				p := pipe.FromSlice(rebuilds)
				p = p.ParDo(RundexReadParallelism, func(in rundex.Rebuild, out chan<- rundex.Rebuild) {
					_, err := butler.Fetch(context.Background(), in.RunID, in.WasSmoketest(), rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrap(err, "downloading logs"))
						return
					}
					out <- in
				})
				// TODO: Instead of a ticker, gracefully handle retriable errors on the API.
				ticker := time.Tick(time.Second / 15) // The Gemini Flash limit is around 15 QPS.
				type summarizedRebuild struct {
					Rebuild rundex.Rebuild
					Summary string
				}
				summaries := pipe.ParInto(LLMRequestParallelism, p, func(in rundex.Rebuild, out chan<- summarizedRebuild) {
					const uploadBytesLimit = 100_000
					assets, err := localfiles.AssetStore(in.RunID)
					if err != nil {
						log.Println(errors.Wrapf(err, "creating asset store for runid: %s", in.RunID))
						return
					}
					r, err := assets.Reader(ctx, rebuild.DebugLogsAsset.For(in.Target()))
					if err != nil {
						log.Println(errors.Wrapf(err, "opening logs for %s", in.ID()))
						return
					}
					defer r.Close()
					content, err := io.ReadAll(r)
					if err != nil {
						log.Println(errors.Wrap(err, "reading logs"))
						return
					}
					logs := string(content)
					if len(logs) > uploadBytesLimit {
						logs = "...(truncated)..." + logs[len(logs)-uploadBytesLimit:]
					}
					parts := []*genai.Part{
						{Text: "Please summarize this rebuild failure in one sentence."},
						{Text: logs},
					}
					<-ticker
					txt, err := llm.GenerateTextContent(ctx, aiClient, llm.GeminiFlash, config, parts...)
					if err != nil {
						log.Println(errors.Wrap(err, "sending message"))
						return
					}
					out <- summarizedRebuild{Rebuild: in, Summary: string(txt)}
					log.Println("Summary: ", txt)
				})
				var parts []*genai.Part
				log.Printf("Summarizing %d rebuild failures", len(rebuilds))
				for s := range summaries.Out() {
					if s.Summary == "" {
						continue
					}
					parts = append(parts, &genai.Part{Text: s.Summary})
				}
				log.Printf("Finished summarizing, Asking for categories based on %d summaries.", len(parts))
				// TODO: Give more structure to the expected output format to make it easier parsing the response.
				parts = append([]*genai.Part{{Text: "Based on the following error summaries, please provide 1 to 5 classes of failures you think are happening."}}, parts...)
				<-ticker
				txt, err := llm.GenerateTextContent(ctx, aiClient, llm.GeminiFlash, config, parts...)
				if err != nil {
					log.Println(errors.Wrap(err, "classifying summaries"))
					return
				}
				log.Println(string(txt))
				log.Println("Grouping completed.")
			},
		},
	}

}

func NewGlobalCmds(app *tview.Application, rb *rebuilder.Rebuilder, modalFn modalFnType, butler localfiles.Butler, aiClient *genai.Client, buildDefs rebuild.LocatableAssetStore, dex rundex.Reader, benches benchmark.Repository) []GlobalCmd {
	return []GlobalCmd{
		{
			Short:  "restart rebuilder",
			Hotkey: 'r',
			Func:   func(ctx context.Context) { rb.Restart(ctx) },
		},
		{
			Short:  "kill rebuilder",
			Hotkey: 'x',
			Func: func(_ context.Context) {
				rb.Kill()
			},
		},
		{
			Short:  "attach",
			Hotkey: 'a',
			Func: func(ctx context.Context) {
				if err := rb.Attach(ctx); err != nil {
					log.Println(err)
				}
			},
		},
		{
			Short:  "benchmark",
			Hotkey: 'b',
			Func: func(ctx context.Context) {
				var bench string
				{
					all, err := benches.List()
					if err != nil {
						log.Println(errors.Wrap(err, "listing benchmarks"))
						return
					}
					choice, opts, selected := choice.Choice(all)
					exitFunc := modalFn(choice, opts)
					bench = <-selected
					go app.QueueUpdateDraw(exitFunc)
				}
				wdex, ok := dex.(rundex.Writer)
				if !ok {
					log.Println(errors.New("Cannot run benchmark with non-local rundex client."))
					return
				}
				set, err := benchmark.ReadBenchmark(bench)
				if err != nil {
					log.Println(errors.Wrap(err, "reading benchmark"))
					return
				}
				var runID string
				{
					now := time.Now().UTC()
					runID = now.Format(time.RFC3339)
					wdex.WriteRun(ctx, rundex.FromRun(schema.Run{
						ID:            runID,
						BenchmarkName: filepath.Base(bench),
						BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
						Type:          string(schema.SmoketestMode),
						Created:       now,
					}))
				}
				verdictChan, err := rb.RunBench(ctx, set, runID)
				if err != nil {
					log.Println(errors.Wrap(err, "running benchmark"))
					return
				}
				var successes int
				for v := range verdictChan {
					if v.Message == "" {
						successes += 1
					}
					wdex.WriteRebuild(ctx, rundex.NewRebuildFromVerdict(v, "local", runID, time.Now().UTC()))
				}
				log.Printf("Finished benchmark %s with %d successes.", bench, successes)
			},
		},
	}
}
