package cmd

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/version"
	"github.com/spf13/cobra"
)

const aiBase = "/accounts/{account_id}/ai"

var aiFinetuneRequired = []string{"adapter_config.json", "adapter_model.safetensors"}

var aiModelCols = []platCol{
	{H: "name", Path: "name"},
	{H: "task", Path: "task.name"},
	{H: "description", Path: "description", W: 70},
}

func init() {
	models := platGroup("models", "Browse the Workers AI model catalog", "", []string{"model"},
		platSpecs(
			platSpec{Use: "list", Short: "List catalog models", Aliases: []string{"ls"}, Path: aiBase + "/models/search", List: true,
				Flags: func(c *cobra.Command) {
					c.Flags().String("task", "", "Only models for this task (e.g. \"Text Generation\")")
					c.Flags().String("author", "", "Only models by this author")
					c.Flags().String("search", "", "Search model names and descriptions")
					c.Flags().Bool("hide-experimental", false, "Hide experimental models")
				},
				Query: func(c *cobra.Command, _ []string, q url.Values) error {
					platQueryStr(c, q, "task", "task", "author", "author", "search", "search")
					if h, _ := c.Flags().GetBool("hide-experimental"); h {
						q.Set("hide_experimental", "true")
					}
					q.Set("per_page", "100")
					return nil
				},
				Cols: aiModelCols, Title: "🤖 %d models", Product: "Workers AI"},
			platSpec{Use: "search <words>", Short: "Search the catalog", Path: aiBase + "/models/search", ExtraArgs: 1, List: true,
				Query: func(c *cobra.Command, args []string, q url.Values) error {
					q.Set("search", args[0])
					q.Set("per_page", "100")
					return nil
				},
				Cols: aiModelCols, Title: "🤖 %d models", Product: "Workers AI"},
			platSpec{Use: "get <model>", Short: "Show a catalog model", Aliases: []string{"info", "show"}, Path: aiBase + "/models/search", ExtraArgs: 1, Product: "Workers AI",
				Run: runAIModelGet},
			platSpec{Use: "schema <model>", Short: "Show a model's input/output JSON schema", Path: aiBase + "/models/schema", ExtraArgs: 1,
				Query:   func(c *cobra.Command, args []string, q url.Values) error { q.Set("model", args[0]); return nil },
				Product: "Workers AI", Print: func(_ *cobra.Command, _ []string, raw json.RawMessage) error { return printBody(raw, nil) }},
		)...)

	tasks := platCommand(platSpec{Use: "tasks", Short: "List model tasks", Path: aiBase + "/tasks/search", List: true,
		Cols: []platCol{{H: "name", Path: "name"}, {H: "id", Path: "id"}, {H: "description", Path: "description", W: 70}}, Title: "🤖 %d tasks", Product: "Workers AI"})
	authors := platCommand(platSpec{Use: "authors", Short: "List model authors", Path: aiBase + "/authors/search", List: true,
		Cols: []platCol{{H: "name", Path: "name"}, {H: "description", Path: "description", W: 70}}, Title: "🤖 %d authors", Product: "Workers AI"})

	finetuneCols := []platCol{{H: "id", Path: "id"}, {H: "name", Path: "name"}, {H: "model", Path: "model"}, {H: "description", Path: "description", W: 40}, {H: "created", Path: "created_at"}}
	finetune := platGroup("finetune", "Manage LoRA finetunes", "", []string{"finetunes"},
		append(platSpecs(
			platSpec{Use: "list", Short: "List your finetunes", Aliases: []string{"ls"}, Path: aiBase + "/finetunes", Cols: finetuneCols, Title: "🎛  %d finetunes", Product: "Workers AI"},
			platSpec{Use: "public", Short: "List public finetunes", Path: aiBase + "/finetunes/public", List: true, Cols: finetuneCols, Title: "🎛  %d public finetunes", Product: "Workers AI"},
			platSpec{Use: "delete <finetune-id>", Short: "Delete a finetune", Aliases: []string{"rm"}, Method: "DELETE", Path: aiBase + "/finetunes/{finetune_id}",
				Confirm: "delete finetune %s", Done: "Deleted finetune %s", Product: "Workers AI"},
		), aiFinetuneCreate())...)

	aiCmd := platGroup("ai", "Workers AI: models, inference, finetunes",
		`Workers AI.

  cfctl ai models list|search|get|schema    the model catalog
  cfctl ai run <model> --prompt "..."       run inference (billed; --stream supported)
  cfctl ai finetune list|create|delete      LoRA finetunes
  cfctl ai tasks | authors                  catalog facets
  cfctl ai markdown <file>                  convert a document to Markdown

Generated equivalents: 'cfctl api workers-ai ...', 'cfctl api workers-ai-finetune ...'.`, nil,
		models, tasks, authors, finetune, aiRunCmd(), aiMarkdownCmd())
	rootCmd.AddCommand(aiCmd)
}

func runAIModelGet(c *cobra.Command, args []string) error {
	ctx := context.Background()
	s, err := newAPISession(c)
	if err != nil {
		return err
	}
	path, _, err := platFill(ctx, s, aiBase+"/models/search", nil)
	if err != nil {
		return err
	}
	raw, err := platAll(ctx, s, path, url.Values{"search": {path2base(args[0])}, "per_page": {"100"}})
	if err != nil {
		return platErr("Workers AI", err)
	}
	var ms []map[string]any
	if err := platDecode(raw, &ms); err != nil {
		return err
	}
	for _, m := range ms {
		if m["name"] == args[0] || m["id"] == args[0] {
			if jsonOutput {
				return printJSONValue(m)
			}
			platDetail("🤖 "+platStr(m["name"]), m, []platCol{
				{H: "Name", Path: "name"}, {H: "ID", Path: "id"}, {H: "Task", Path: "task.name"},
				{H: "Source", Path: "source"}, {H: "Description", Path: "description", W: 200},
			})
			if props, ok := m["properties"].([]any); ok {
				for _, p := range props {
					fmt.Printf("  %s  %s\n", ui.SubtleStyle.Render(platStr(platGet(p, "property_id"))+":"), platTrunc(platStr(platGet(p, "value")), 100))
				}
			}
			return nil
		}
	}
	return fmt.Errorf("model %q not found; try 'cfctl ai models search %s'", args[0], filepath.Base(args[0]))
}

func aiFinetuneCreate() *cobra.Command {
	return platCommand(platSpec{
		Use:   "create <model> <name> <folder>",
		Short: "Create a finetune and upload its LoRA adapter files",
		Long: `Create a finetune for a catalog model and upload the adapter from <folder>, which must
contain adapter_config.json and adapter_model.safetensors.`,
		Path: aiBase + "/finetunes", ExtraArgs: 3,
		Flags: func(c *cobra.Command) { c.Flags().String("description", "", "Finetune description") },
		Run: func(c *cobra.Command, args []string) error {
			model, name, folder := args[0], args[1], args[2]
			for _, f := range aiFinetuneRequired {
				if _, err := os.Stat(filepath.Join(folder, f)); err != nil {
					return fmt.Errorf("asset missing: %s needs %s", folder, strings.Join(aiFinetuneRequired, " and "))
				}
			}
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			base, _, err := platFill(ctx, s, aiBase+"/finetunes", nil)
			if err != nil {
				return err
			}
			desc, _ := c.Flags().GetString("description")
			raw, err := platDo(ctx, s, "POST", base, nil, map[string]any{"model": model, "name": name, "description": desc})
			if err != nil {
				return platErr("Workers AI", err)
			}
			var ft map[string]any
			if err := platDecode(raw, &ft); err != nil {
				return err
			}
			id := platStr(ft["id"])
			for _, f := range aiFinetuneRequired {
				body, ct, err := api.BuildMultipart([]api.FormField{
					{Name: "file_name", Value: f},
					{Name: "file", File: filepath.Join(folder, f), FileName: f, ContentType: "application/octet-stream"},
				})
				if err != nil {
					return err
				}
				if _, err := s.c.Do(ctx, api.Request{Method: "POST", Path: base + "/" + url.PathEscape(id) + "/finetune-assets", Body: body, ContentType: ct}); err != nil {
					return fmt.Errorf("finetune %s created, but uploading %s failed: %w", id, f, platErr("Workers AI", err))
				}
				if !jsonOutput {
					fmt.Fprintln(os.Stderr, ui.Info("Uploaded "+f))
				}
			}
			if jsonOutput {
				return printJSONValue(ft)
			}
			fmt.Println(ui.Success(fmt.Sprintf("Finetune %s (%s) is ready for %s", name, id, model)))
			return nil
		},
	})
}

func aiMarkdownCmd() *cobra.Command {
	return platCommand(platSpec{
		Use:   "markdown <file>...",
		Short: "Convert documents (PDF, images, HTML, Office, ...) to Markdown",
		Path:  aiBase + "/tomarkdown", ExtraArgs: 1, OptionalArgs: 50,
		Run: func(c *cobra.Command, args []string) error {
			var fields []api.FormField
			for _, f := range args {
				fields = append(fields, api.FormField{Name: "files", File: f, FileName: filepath.Base(f)})
			}
			body, ct, err := api.BuildMultipart(fields)
			if err != nil {
				return err
			}
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, aiBase+"/tomarkdown", nil)
			if err != nil {
				return err
			}
			resp, err := s.c.Do(ctx, api.Request{Method: "POST", Path: path, Body: body, ContentType: ct})
			if err != nil {
				return platErr("Workers AI", err)
			}
			raw := json.RawMessage(resp.Body)
			if resp.Envelope != nil {
				raw = resp.Envelope.Result
			}
			if jsonOutput {
				return printBody(raw, nil)
			}
			var docs []map[string]any
			if err := platDecode(raw, &docs); err != nil {
				return err
			}
			for i, d := range docs {
				if len(docs) > 1 {
					fmt.Println(ui.TitleStyle.Render("# " + platStr(d["name"])))
				}
				if e := platStr(d["error"]); e != "" {
					fmt.Fprintln(os.Stderr, ui.Err(platStr(d["name"])+": "+e))
				}
				fmt.Println(platStr(d["data"]))
				if i < len(docs)-1 {
					fmt.Println()
				}
			}
			return nil
		},
	})
}

// ai run -------------------------------------------------------------------

func aiRunCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "run <model>",
		Short: "Run a model (Workers AI inference; billed per use)",
		Long: `Run inference on a Workers AI model.

Input:
  --prompt "text"        {"prompt": ...} (text generation, image generation)
  --system "text"        with --prompt, sends {"messages": [system, user]} instead
  --data JSON|@file|-    the exact input object (see 'cfctl ai models schema <model>')
  --file path            binary input (audio/images), sent as the raw request body
Output:
  Text responses print the generated text; --json prints the whole result.
  Binary responses (images, audio) are written to --output.
  --stream streams tokens as they're generated (text-generation models).

This is a paid API call (it spends neurons); it's refused in --read-only mode.`,
		Example: `  cfctl ai run @cf/meta/llama-3.1-8b-instruct --prompt "Write a haiku about DNS"
  cfctl ai run @cf/meta/llama-3.1-8b-instruct --prompt "hi" --stream
  cfctl ai run @cf/black-forest-labs/flux-1-schnell --prompt "a cloud" --output cloud.jpg
  cfctl ai run @cf/openai/whisper --file talk.mp3 --json`,
		Args: cobra.ExactArgs(1),
		RunE: runAIRun,
	}
	c.Flags().StringP("prompt", "p", "", "Prompt text")
	c.Flags().String("system", "", "System message (with --prompt: uses chat messages)")
	c.Flags().String("data", "", "Model input as JSON: string, @file, or - for stdin")
	c.Flags().String("file", "", "Binary input file sent as the request body")
	c.Flags().Bool("stream", false, "Stream the response (server-sent events)")
	c.Flags().Int("max-tokens", 0, "max_tokens for text generation")
	c.Flags().Float64("temperature", -1, "Sampling temperature")
	c.Flags().StringP("output", "o", "", "Write binary output (image/audio) to this file")
	return c
}

func aiRunInput(c *cobra.Command) ([]byte, string, error) {
	prompt, _ := c.Flags().GetString("prompt")
	system, _ := c.Flags().GetString("system")
	file, _ := c.Flags().GetString("file")
	stream, _ := c.Flags().GetBool("stream")
	set := 0
	for _, f := range []string{"prompt", "data", "file"} {
		if c.Flags().Changed(f) {
			set++
		}
	}
	if set != 1 {
		return nil, "", fmt.Errorf("give exactly one of --prompt, --data, or --file")
	}
	if file != "" {
		b, err := os.ReadFile(file)
		return b, "application/octet-stream", err
	}
	in := map[string]any{}
	if c.Flags().Changed("data") {
		d, _ := c.Flags().GetString("data")
		b, err := api.ReadData(d, os.Stdin)
		if err != nil {
			return nil, "", err
		}
		if err := json.Unmarshal(b, &in); err != nil {
			return nil, "", fmt.Errorf("--data must be a JSON object: %w", err)
		}
	} else if system != "" {
		in["messages"] = []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": prompt}}
	} else {
		in["prompt"] = prompt
	}
	if mt, _ := c.Flags().GetInt("max-tokens"); mt > 0 {
		in["max_tokens"] = mt
	}
	if t, _ := c.Flags().GetFloat64("temperature"); t >= 0 {
		in["temperature"] = t
	}
	if stream {
		in["stream"] = true
	}
	b, err := json.Marshal(in)
	return b, "application/json", err
}

func runAIRun(c *cobra.Command, args []string) error {
	body, ct, err := aiRunInput(c)
	if err != nil {
		return err
	}
	ctx := context.Background()
	s, err := newAPISession(c)
	if err != nil {
		return err
	}
	base, _, err := platFill(ctx, s, aiBase+"/run", nil)
	if err != nil {
		return err
	}
	// Model names contain slashes (@cf/meta/...): keep them as path segments.
	path := base + "/" + strings.TrimPrefix(args[0], "/")
	if stream, _ := c.Flags().GetBool("stream"); stream {
		return aiStream(ctx, s, path, body, ct)
	}
	resp, err := s.c.Do(ctx, api.Request{Method: "POST", Path: path, Body: body, ContentType: ct})
	if err != nil {
		return platErr("Workers AI", err)
	}
	out, _ := c.Flags().GetString("output")
	if resp.Envelope == nil && !resp.IsJSON() {
		// Binary output (image/audio).
		if out == "" {
			return fmt.Errorf("the model returned %s (%d bytes); pass --output <file> to save it", resp.ContentType, len(resp.Body))
		}
		if err := os.WriteFile(out, resp.Body, 0o644); err != nil {
			return err
		}
		if jsonOutput {
			return printJSONValue(map[string]any{"output": out, "bytes": len(resp.Body), "content_type": resp.ContentType})
		}
		fmt.Println(ui.Success(fmt.Sprintf("Wrote %s (%d bytes, %s)", out, len(resp.Body), resp.ContentType)))
		return nil
	}
	raw := json.RawMessage(resp.Body)
	if resp.Envelope != nil {
		raw = resp.Envelope.Result
	}
	if jsonOutput {
		return printBody(raw, nil)
	}
	var res map[string]any
	if json.Unmarshal(raw, &res) == nil {
		if txt, ok := res["response"].(string); ok {
			fmt.Println(txt)
			return nil
		}
		if ch, ok := res["choices"].([]any); ok && len(ch) > 0 {
			if m := platGet(ch[0], "message.content"); m != nil {
				fmt.Println(platStr(m))
				return nil
			}
		}
		if txt, ok := res["text"].(string); ok {
			fmt.Println(txt)
			return nil
		}
		if img, ok := res["image"].(string); ok && out != "" {
			if err := aiWriteBase64(out, img); err != nil {
				return err
			}
			fmt.Println(ui.Success("Wrote " + out))
			return nil
		}
	}
	return printBody(raw, nil)
}

// aiStream posts with stream=true and prints SSE "response" deltas as they
// arrive. It goes through the shared HTTP client (read-only guard, debug).
func aiStream(ctx context.Context, s *apiSession, path string, body []byte, ct string) error {
	token, err := config.LoadToken()
	if err != nil {
		return err
	}
	u, err := s.c.URL(path, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "cfctl/"+version.Version)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.c.HTTP.Do(req)
	if err != nil {
		var ro *api.ReadOnlyError
		if errors.As(err, &ro) {
			return ro
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("Workers AI request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		e := &api.Error{Status: resp.StatusCode, Method: "POST", Path: req.URL.Path}
		var env api.Envelope
		if json.Unmarshal(data, &env) == nil {
			e.Errors = env.Errors
		}
		return platErr("Workers AI", e)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if jsonOutput {
			fmt.Println(data)
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if t, ok := ev["response"].(string); ok {
			fmt.Print(t)
		} else if ch, ok := ev["choices"].([]any); ok && len(ch) > 0 {
			fmt.Print(platStr(platGet(ch[0], "delta.content")))
		}
	}
	if !jsonOutput {
		fmt.Println()
	}
	return sc.Err()
}

func aiWriteBase64(path, b64 string) error {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("the model's image isn't valid base64: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// path2base is the last segment of a model name ("@cf/meta/x" → "x"), which
// the catalog search matches reliably.
func path2base(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}
