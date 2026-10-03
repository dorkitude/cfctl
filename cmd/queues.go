package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var queuesCmd = &cobra.Command{
	Use:     "queues",
	Aliases: []string{"queue"},
	Short:   "Manage Queues, consumers, messages, and event subscriptions",
	Long: `Manage Cloudflare Queues: queues, Worker and HTTP pull consumers, delivery
pause/resume, purge, sending and pulling messages, and event subscriptions.

A queue can be given by name or ID everywhere.

Examples:
  cfctl queues list
  cfctl queues create jobs --delivery-delay 5
  cfctl queues consumer add jobs my-worker --batch-size 50 --message-retries 5
  cfctl queues send jobs '{"task":"resize"}' --content-type json
  cfctl queues pause-delivery jobs
  cfctl queues subscription create jobs --source r2 --events object.create --name uploads

Generated equivalents: cfctl api queue <op>.`,
}

var queueCols = []stCol{
	{Header: "NAME", Path: "queue_name"},
	{Header: "ID", Path: "queue_id"},
	{Header: "CREATED", Path: "created_on"},
	{Header: "PRODUCERS", Path: "producers_total_count"},
	{Header: "CONSUMERS", Path: "consumers_total_count"},
	{Header: "PAUSED", Path: "settings.delivery_paused"},
}

// queueID resolves a queue name or ID.
func queueID(c *stClient, arg string) (string, error) {
	return c.resolve("queue", c.p("queues"), nil, arg, "queue_id", "queue_name")
}

// queuePath resolves args[0] and returns /queues/{id}/<segs...>.
func queuePath(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		id, err := queueID(c, args[0])
		if err != nil {
			return "", nil, err
		}
		return c.p("queues", append([]string{id}, segs...)...), nil, nil
	}
}

var queuesListCmd = sbListCmd("list", "List queues", "", cobra.NoArgs,
	sbFixed("queues"), "📬 %d queues", "No queues found", queueCols)

var queuesGetCmd = func() *cobra.Command {
	c := sbGetCmd("get <queue>", "Show a queue (settings, producers, consumers)", "", "📬 Queue", cobra.ExactArgs(1), queuePath())
	c.Aliases = []string{"info"}
	return c
}()

func queueSettingsFlags(cmd *cobra.Command) {
	cmd.Flags().Int("delivery-delay", 0, "Seconds to delay delivery of every message")
	cmd.Flags().Int("message-retention-period", 0, "Seconds an unconsumed message is kept (60-1209600)")
	stDataFlag(cmd)
}

func queueSettingsBody(cmd *cobra.Command) (map[string]any, error) {
	body, err := stBody(cmd)
	if err != nil {
		return nil, err
	}
	stFlagInt(cmd, body, "delivery-delay", "settings.delivery_delay")
	stFlagInt(cmd, body, "message-retention-period", "settings.message_retention_period")
	return body, nil
}

var queuesCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a queue",
	Long: `Create a queue.

Examples:
  cfctl queues create jobs
  cfctl queues create jobs --delivery-delay 10 --message-retention-period 86400
  cfctl queues create eu-jobs --jurisdiction eu`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := queueSettingsBody(cmd)
		if err != nil {
			return err
		}
		body["queue_name"] = args[0]
		stFlagStr(cmd, body, "jurisdiction", "jurisdiction")
		return sbSendShow(c, "POST", c.p("queues"), nil, body, "Created queue "+args[0])
	}),
}

var queuesUpdateCmd = &cobra.Command{
	Use:   "update <queue>",
	Short: "Update a queue's settings (partial update)",
	Long: `Update a queue's settings. Only the flags you pass change.

Examples:
  cfctl queues update jobs --delivery-delay 0
  cfctl queues update jobs --message-retention-period 345600
  cfctl queues update jobs --data '{"settings":{"delivery_paused":false}}'`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := queueSettingsBody(cmd)
		if err != nil {
			return err
		}
		stFlagStr(cmd, body, "name", "queue_name")
		if len(body) == 0 {
			return fmt.Errorf("nothing to update: pass --delivery-delay, --message-retention-period, --name, or --data")
		}
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		return sbSendShow(c, "PATCH", c.p("queues", id), nil, body, "Updated queue "+args[0])
	}),
}

var queuesDeleteCmd = sbDeleteCmd("delete <queue>", "Delete a queue", "", cobra.ExactArgs(1),
	func(a []string) string { return "queue " + a[0] }, queuePath())

// queueSetPaused flips settings.delivery_paused, keeping the other settings.
func queueSetPaused(paused bool) func(cmd *cobra.Command, c *stClient, args []string) error {
	return func(cmd *cobra.Command, c *stClient, args []string) error {
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.get(c.p("queues", id), nil)
		if err != nil {
			return err
		}
		var q struct {
			Settings map[string]any `json:"settings"`
		}
		_ = json.Unmarshal(raw, &q)
		if q.Settings == nil {
			q.Settings = map[string]any{}
		}
		q.Settings["delivery_paused"] = paused
		verb := "Resumed"
		if paused {
			verb = "Paused"
		}
		return sbSend(c, "PATCH", c.p("queues", id), nil, map[string]any{"settings": q.Settings}, verb+" delivery for "+args[0])
	}
}

var queuesPauseCmd = &cobra.Command{
	Use:   "pause-delivery <queue>",
	Short: "Pause message delivery to consumers (messages keep queueing)",
	Args:  cobra.ExactArgs(1),
	RunE:  sbRun(queueSetPaused(true)),
}

var queuesResumeCmd = &cobra.Command{
	Use:   "resume-delivery <queue>",
	Short: "Resume message delivery to consumers",
	Args:  cobra.ExactArgs(1),
	RunE:  sbRun(queueSetPaused(false)),
}

var queuesPurgeCmd = &cobra.Command{
	Use:   "purge <queue>",
	Short: "Permanently delete every message in a queue",
	Long: `Start a purge that permanently deletes every message in a queue. Check
progress with 'cfctl queues purge status <queue>'.`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		if err := confirm(cmd, "permanently delete every message in queue "+args[0]); err != nil {
			return err
		}
		return sbSend(c, "POST", c.p("queues", id, "purge"), nil, map[string]any{"delete_messages_permanently": true}, "Purge started for "+args[0])
	}),
}

var queuesPurgeStatusCmd = sbGetCmd("status <queue>", "Show the status of a queue purge", "", "🧹 Purge status", cobra.ExactArgs(1), queuePath("purge"))

var queuesMetricsCmd = sbGetCmd("metrics <queue>", "Show a queue's backlog metrics", "", "📈 Queue metrics", cobra.ExactArgs(1), queuePath("metrics"))

// --- consumers ----------------------------------------------------------------

var queuesConsumerCmd = &cobra.Command{
	Use:     "consumer",
	Aliases: []string{"consumers"},
	Short:   "Manage a queue's Worker and HTTP pull consumers",
}

var queuesConsumerListCmd = sbListCmd("list <queue>", "List a queue's consumers", "", cobra.ExactArgs(1),
	queuePath("consumers"), "📥 %d consumers", "No consumers", []stCol{
		{Header: "TYPE", Path: "type"},
		{Header: "SCRIPT", Path: "", Fmt: sbFirst("script_name", "script", "service")},
		{Header: "ID", Path: "consumer_id"},
		{Header: "BATCH", Path: "settings.batch_size"},
		{Header: "RETRIES", Path: "settings.max_retries"},
		{Header: "DLQ", Path: "dead_letter_queue"},
	})

var queuesConsumerGetCmd = sbGetCmd("get <queue> <consumer-id>", "Show a consumer", "", "📥 Consumer", cobra.ExactArgs(2),
	func(c *stClient, args []string) (string, url.Values, error) {
		id, err := queueID(c, args[0])
		if err != nil {
			return "", nil, err
		}
		return c.p("queues", id, "consumers", args[1]), nil, nil
	})

func queueConsumerFlags(cmd *cobra.Command, worker bool) {
	cmd.Flags().Int("batch-size", 0, "Maximum messages per batch")
	cmd.Flags().Int("message-retries", 0, "Maximum retries per message")
	cmd.Flags().Int("retry-delay", 0, "Seconds before a retried message is redelivered")
	cmd.Flags().String("dead-letter-queue", "", "Queue for messages that exhaust their retries")
	if worker {
		cmd.Flags().Int("batch-timeout", 0, "Seconds to wait for a batch to fill (max_wait_time_ms = seconds × 1000)")
		cmd.Flags().Int("max-concurrency", 0, "Maximum concurrent consumer invocations")
	} else {
		cmd.Flags().Int("visibility-timeout", 0, "Milliseconds a pulled message is leased")
	}
	stDataFlag(cmd)
}

func queueConsumerBody(cmd *cobra.Command, body map[string]any) {
	stFlagInt(cmd, body, "batch-size", "settings.batch_size")
	stFlagInt(cmd, body, "message-retries", "settings.max_retries")
	stFlagInt(cmd, body, "retry-delay", "settings.retry_delay")
	stFlagInt(cmd, body, "max-concurrency", "settings.max_concurrency")
	stFlagInt(cmd, body, "visibility-timeout", "settings.visibility_timeout_ms")
	stFlagStr(cmd, body, "dead-letter-queue", "dead_letter_queue")
	if cmd.Flags().Lookup("batch-timeout") != nil && cmd.Flags().Changed("batch-timeout") {
		s, _ := cmd.Flags().GetInt("batch-timeout")
		stSet(body, "settings.max_wait_time_ms", s*1000)
	}
}

func newQueueConsumerAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <queue> <script>",
		Short: "Add a Worker consumer to a queue",
		Long: `Add a Worker as a consumer of a queue.

Examples:
  cfctl queues consumer add jobs my-worker
  cfctl queues consumer add jobs my-worker --batch-size 50 --batch-timeout 5 --message-retries 3 --dead-letter-queue jobs-dlq`,
		Args: cobra.ExactArgs(2),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			body, err := stBody(cmd)
			if err != nil {
				return err
			}
			body["type"] = "worker"
			body["script_name"] = args[1]
			queueConsumerBody(cmd, body)
			id, err := queueID(c, args[0])
			if err != nil {
				return err
			}
			return sbSendShow(c, "POST", c.p("queues", id, "consumers"), nil, body, fmt.Sprintf("Added %s as a consumer of %s", args[1], args[0]))
		}),
	}
	queueConsumerFlags(cmd, true)
	return cmd
}

// queueFindConsumer finds a consumer by script name (worker) or type
// (http_pull when script is "").
func queueFindConsumer(c *stClient, qid, script, typ string) (map[string]any, error) {
	raw, err := c.all(c.p("queues", qid, "consumers"), nil)
	if err != nil {
		return nil, err
	}
	for _, it := range stItems(raw) {
		if typ != "" && stString(it["type"]) != typ {
			continue
		}
		if script == "" || script == sbField(it, "script_name", "script", "service") || script == stString(it["consumer_id"]) {
			return it, nil
		}
	}
	if script == "" {
		return nil, fmt.Errorf("no %s consumer found", typ)
	}
	return nil, fmt.Errorf("no consumer %q found", script)
}

func newQueueConsumerRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <queue> <script>",
		Short: "Remove a Worker consumer (by script name or consumer ID)",
		Args:  cobra.ExactArgs(2),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			id, err := queueID(c, args[0])
			if err != nil {
				return err
			}
			cons, err := queueFindConsumer(c, id, args[1], "")
			if err != nil {
				return err
			}
			if err := confirm(cmd, fmt.Sprintf("remove consumer %s from queue %s", args[1], args[0])); err != nil {
				return err
			}
			return sbSend(c, "DELETE", c.p("queues", id, "consumers", stString(cons["consumer_id"])), nil, nil, fmt.Sprintf("Removed consumer %s from %s", args[1], args[0]))
		}),
	}
	stYes(cmd)
	return cmd
}

var queuesConsumerUpdateCmd = &cobra.Command{
	Use:   "update <queue> <script-or-consumer-id>",
	Short: "Change a consumer's settings (the other settings are kept)",
	Long: `Change a consumer's settings. The current consumer is read first, so only
the flags you pass change.

Examples:
  cfctl queues consumer update jobs my-worker --batch-size 100
  cfctl queues consumer update jobs 0123... --dead-letter-queue jobs-dlq`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		cons, err := queueFindConsumer(c, id, args[1], "")
		if err != nil {
			return err
		}
		body := map[string]any{"type": cons["type"]}
		if s := sbField(cons, "script_name", "script"); s != "" && stString(cons["type"]) == "worker" {
			body["script_name"] = s
		}
		if v, ok := cons["dead_letter_queue"]; ok && v != nil && v != "" {
			body["dead_letter_queue"] = v
		}
		if s, ok := cons["settings"].(map[string]any); ok {
			body["settings"] = s
		}
		extra, err := stBody(cmd)
		if err != nil {
			return err
		}
		for k, v := range extra {
			body[k] = v
		}
		queueConsumerBody(cmd, body)
		return sbSendShow(c, "PUT", c.p("queues", id, "consumers", stString(cons["consumer_id"])), nil, body, "Updated consumer "+args[1])
	}),
}

var queuesConsumerHTTPCmd = &cobra.Command{
	Use:   "http",
	Short: "Configure HTTP pull consumers",
}

var queuesConsumerHTTPAddCmd = func() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <queue>",
		Short: "Add an HTTP pull consumer (pull messages with 'cfctl queues pull')",
		Long: `Add an HTTP pull consumer.

Examples:
  cfctl queues consumer http add jobs
  cfctl queues consumer http add jobs --batch-size 20 --visibility-timeout 60000`,
		Args: cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			body, err := stBody(cmd)
			if err != nil {
				return err
			}
			body["type"] = "http_pull"
			queueConsumerBody(cmd, body)
			id, err := queueID(c, args[0])
			if err != nil {
				return err
			}
			return sbSendShow(c, "POST", c.p("queues", id, "consumers"), nil, body, "Added an HTTP pull consumer to "+args[0])
		}),
	}
	queueConsumerFlags(cmd, false)
	return cmd
}()

var queuesConsumerHTTPRemoveCmd = func() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <queue>",
		Short: "Remove the HTTP pull consumer",
		Args:  cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			id, err := queueID(c, args[0])
			if err != nil {
				return err
			}
			cons, err := queueFindConsumer(c, id, "", "http_pull")
			if err != nil {
				return err
			}
			if err := confirm(cmd, "remove the HTTP pull consumer from queue "+args[0]); err != nil {
				return err
			}
			return sbSend(c, "DELETE", c.p("queues", id, "consumers", stString(cons["consumer_id"])), nil, nil, "Removed the HTTP pull consumer from "+args[0])
		}),
	}
	stYes(cmd)
	return cmd
}()

var queuesConsumerWorkerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Configure Worker consumers (same as 'consumer add|remove')",
}

// --- messages -----------------------------------------------------------------

// queueMessage builds one message from a body string.
func queueMessage(body, contentType string) (map[string]any, error) {
	switch contentType {
	case "", "text":
		return map[string]any{"body": body, "content_type": "text"}, nil
	case "json":
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, fmt.Errorf("message body is not valid JSON: %w", err)
		}
		return map[string]any{"body": v, "content_type": "json"}, nil
	}
	return nil, fmt.Errorf("--content-type must be text or json")
}

var queuesSendCmd = &cobra.Command{
	Use:   "send <queue> <body>",
	Short: "Send a message to a queue",
	Long: `Send one message. The body is text by default; --content-type json (or
--json-body) sends it as JSON. Use - to read the body from stdin, @file for a file.

Examples:
  cfctl queues send jobs hello
  cfctl queues send jobs '{"task":"resize","id":42}' --json-body
  cfctl queues send jobs @msg.json --content-type json --delay 30`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		data, err := stReadInput(args[1])
		if err != nil {
			return err
		}
		ct, _ := cmd.Flags().GetString("content-type")
		if jb, _ := cmd.Flags().GetBool("json-body"); jb {
			ct = "json"
		}
		msg, err := queueMessage(string(data), ct)
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("delay") {
			d, _ := cmd.Flags().GetInt("delay")
			msg["delay_seconds"] = d
		}
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		return sbSend(c, "POST", c.p("queues", id, "messages"), nil, msg, "Sent a message to "+args[0])
	}),
}

// queueBatchLimit is the most messages per batch request.
var queueBatchLimit = 100

var queuesSendBatchCmd = &cobra.Command{
	Use:   "send-batch <queue> <file.json>",
	Short: "Send messages from a JSON array (batches of 100)",
	Long: `Send many messages from a JSON file (or - for stdin). Each element is either
a message object ({"body": ..., "content_type": "json"|"text", "delay_seconds": N})
or any other JSON value, which is sent as a JSON message.

Examples:
  cfctl queues send-batch jobs messages.json
  cfctl queues send-batch jobs messages.json --delay 60`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		items, err := kvReadArray(args[1])
		if err != nil {
			return err
		}
		msgs := make([]any, 0, len(items))
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				if _, has := m["body"]; has {
					if _, has := m["content_type"]; !has {
						m["content_type"] = "json"
						if _, isStr := m["body"].(string); isStr {
							m["content_type"] = "text"
						}
					}
					msgs = append(msgs, m)
					continue
				}
			}
			msgs = append(msgs, map[string]any{"body": it, "content_type": "json"})
		}
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		for start := 0; start < len(msgs); start += queueBatchLimit {
			end := min(start+queueBatchLimit, len(msgs))
			body := map[string]any{"messages": msgs[start:end]}
			if cmd.Flags().Changed("delay") {
				d, _ := cmd.Flags().GetInt("delay")
				body["delay_seconds"] = d
			}
			if _, err := c.result(stReq{Method: "POST", Path: c.p("queues", id, "messages", "batch"), Body: body}); err != nil {
				return fmt.Errorf("batch %d-%d: %w", start, end-1, err)
			}
		}
		return stEmitValue(map[string]any{"sent": len(msgs)}, func() error {
			fmt.Println(ui.Success(fmt.Sprintf("Sent %d messages to %s", len(msgs), args[0])))
			return nil
		})
	}),
}

func queueMessagesView(title string, raw json.RawMessage) error {
	return stEmit(raw, func() error {
		var r struct {
			Backlog  *float64         `json:"message_backlog_count"`
			Messages []map[string]any `json:"messages"`
		}
		if json.Unmarshal(raw, &r) != nil {
			r.Messages = stItems(raw)
		}
		if r.Backlog != nil {
			fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("Backlog: %d messages", int64(*r.Backlog))))
		}
		stList(title, "No messages", r.Messages, []stCol{
			{Header: "ID", Path: "id"},
			{Header: "LEASE / REF", Path: "", Fmt: sbFirst("lease_id", "ref")},
			{Header: "ATTEMPTS", Path: "attempts"},
			{Header: "BODY", Path: "body", Fmt: sbTrunc(60)},
		})
		return nil
	})
}

var queuesPullCmd = &cobra.Command{
	Use:   "pull <queue>",
	Short: "Pull (lease) messages for an HTTP pull consumer",
	Long: `Pull a batch of messages. Each message is leased for --visibility-timeout
ms; acknowledge it with 'cfctl queues ack <queue> --ack <lease-id>'. Use --json
for full message bodies and lease IDs.

Examples:
  cfctl queues pull jobs
  cfctl queues pull jobs --batch-size 10 --visibility-timeout 30000 --json`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body := map[string]any{}
		stFlagInt(cmd, body, "batch-size", "batch_size")
		stFlagInt(cmd, body, "visibility-timeout", "visibility_timeout_ms")
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "POST", Path: c.p("queues", id, "messages", "pull"), Body: body})
		if err != nil {
			return err
		}
		return queueMessagesView("📨 %d messages pulled", raw)
	}),
}

var queuesPeekCmd = &cobra.Command{
	Use:   "peek <queue>",
	Short: "Look at messages without leasing them",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body := map[string]any{}
		stFlagInt(cmd, body, "batch-size", "batch_size")
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "POST", Path: c.p("queues", id, "messages", "peek"), Body: body})
		if err != nil {
			return err
		}
		return queueMessagesView("👀 %d messages", raw)
	}),
}

var queuesAckCmd = &cobra.Command{
	Use:   "ack <queue>",
	Short: "Acknowledge and/or retry pulled messages by lease ID",
	Long: `Acknowledge processed messages and retry failed ones, by lease ID.

Examples:
  cfctl queues ack jobs --ack LEASE1,LEASE2
  cfctl queues ack jobs --retry LEASE3 --retry-delay 60`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		acks, _ := cmd.Flags().GetStringSlice("ack")
		retries, _ := cmd.Flags().GetStringSlice("retry")
		if len(acks)+len(retries) == 0 {
			return fmt.Errorf("pass --ack and/or --retry lease IDs")
		}
		body := map[string]any{"acks": []any{}, "retries": []any{}}
		for _, l := range acks {
			body["acks"] = append(body["acks"].([]any), map[string]any{"lease_id": l})
		}
		for _, l := range retries {
			r := map[string]any{"lease_id": l}
			if cmd.Flags().Changed("retry-delay") {
				d, _ := cmd.Flags().GetInt("retry-delay")
				r["delay_seconds"] = d
			}
			body["retries"] = append(body["retries"].([]any), r)
		}
		id, err := queueID(c, args[0])
		if err != nil {
			return err
		}
		return sbSend(c, "POST", c.p("queues", id, "messages", "ack"), nil, body, fmt.Sprintf("Acked %d, retried %d", len(acks), len(retries)))
	}),
}

// --- event subscriptions ------------------------------------------------------

var queuesSubscriptionCmd = &cobra.Command{
	Use:     "subscription",
	Aliases: []string{"subscriptions"},
	Short:   "Manage event subscriptions that deliver platform events to a queue",
}

var subscriptionCols = []stCol{
	{Header: "NAME", Path: "name"},
	{Header: "ID", Path: "id"},
	{Header: "SOURCE", Path: "source.type"},
	{Header: "EVENTS", Path: "events"},
	{Header: "QUEUE", Path: "destination.queue_id"},
	{Header: "ENABLED", Path: "enabled"},
}

var queuesSubscriptionListCmd = &cobra.Command{
	Use:   "list [queue]",
	Short: "List event subscriptions (all, or those delivering to a queue)",
	Args:  cobra.MaximumNArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		raw, err := c.all(c.p("event_subscriptions/subscriptions"), nil)
		if err != nil {
			return err
		}
		items := stItems(raw)
		if len(args) == 1 {
			qid, err := queueID(c, args[0])
			if err != nil {
				return err
			}
			var keep []map[string]any
			for _, it := range items {
				if stString(stGet(it, "destination.queue_id")) == qid {
					keep = append(keep, it)
				}
			}
			items = keep
			b, _ := json.Marshal(items)
			raw = b
			if items == nil {
				raw = json.RawMessage("[]")
			}
		}
		return stEmit(raw, func() error {
			stList("🔔 %d event subscriptions", "No event subscriptions", items, subscriptionCols)
			return nil
		})
	}),
}

var queuesSubscriptionGetCmd = sbGetCmd("get <subscription-id>", "Show an event subscription", "", "🔔 Event subscription", cobra.ExactArgs(1),
	func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("event_subscriptions/subscriptions", args[0]), nil, nil
	})

func subscriptionFlags(cmd *cobra.Command, create bool) {
	cmd.Flags().String("name", "", "Subscription name")
	cmd.Flags().StringSlice("events", nil, "Event types to deliver (comma-separated)")
	cmd.Flags().Bool("enabled", true, "Whether the subscription is active")
	if create {
		cmd.Flags().String("source", "", "Event source: images, kv, r2, superSlurper, vectorize, workersAi.model, workersBuilds.worker, workers.script, workflows.workflow")
		cmd.Flags().String("model-name", "", "Workers AI model (source workersAi.model)")
		cmd.Flags().String("worker-name", "", "Worker name (source workersBuilds.worker)")
		cmd.Flags().String("workflow-name", "", "Workflow name (source workflows.workflow)")
		cmd.Flags().String("script-tag", "", "Worker script tag (source workers.script)")
	}
	stDataFlag(cmd)
}

var queuesSubscriptionCreateCmd = func() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <queue>",
		Short: "Create an event subscription delivering to a queue",
		Long: `Create an event subscription that delivers events from a source to a queue.

Examples:
  cfctl queues subscription create jobs --source r2 --events bucket.created,bucket.deleted --name r2-events
  cfctl queues subscription create jobs --source workflows.workflow --workflow-name my-flow --events instance.completed --name flow-done
  cfctl queues subscription create jobs --source workersAi.model --model-name @cf/meta/llama-3.1-8b-instruct --events batch.completed --name ai`,
		Args: cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			body, err := stBody(cmd)
			if err != nil {
				return err
			}
			stFlagStr(cmd, body, "name", "name")
			stFlagList(cmd, body, "events", "events")
			en, _ := cmd.Flags().GetBool("enabled")
			body["enabled"] = en
			stFlagStr(cmd, body, "source", "source.type")
			stFlagStr(cmd, body, "model-name", "source.model_name")
			stFlagStr(cmd, body, "worker-name", "source.worker_name")
			stFlagStr(cmd, body, "workflow-name", "source.workflow_name")
			stFlagStr(cmd, body, "script-tag", "source.script_tag")
			if stGet(body, "source.type") == nil {
				return fmt.Errorf("--source is required")
			}
			if body["events"] == nil {
				return fmt.Errorf("--events is required")
			}
			if body["name"] == nil {
				body["name"] = fmt.Sprintf("%s-%s", args[0], strings.ReplaceAll(stString(stGet(body, "source.type")), ".", "-"))
			}
			qid, err := queueID(c, args[0])
			if err != nil {
				return err
			}
			body["destination"] = map[string]any{"type": "queues.queue", "queue_id": qid}
			return sbSendShow(c, "POST", c.p("event_subscriptions/subscriptions"), nil, body, "Created event subscription "+stString(body["name"]))
		}),
	}
	subscriptionFlags(cmd, true)
	return cmd
}()

var queuesSubscriptionUpdateCmd = func() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <subscription-id>",
		Short: "Update an event subscription (name, events, enabled)",
		Long: `Update an event subscription. Only the flags you pass change.

Examples:
  cfctl queues subscription update 0123... --enabled=false
  cfctl queues subscription update 0123... --events object.create,object.delete`,
		Args: cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			body, err := stBody(cmd)
			if err != nil {
				return err
			}
			stFlagStr(cmd, body, "name", "name")
			stFlagList(cmd, body, "events", "events")
			stFlagBool(cmd, body, "enabled", "enabled")
			if len(body) == 0 {
				return fmt.Errorf("nothing to update: pass --name, --events, --enabled, or --data")
			}
			return sbSendShow(c, "PATCH", c.p("event_subscriptions/subscriptions", args[0]), nil, body, "Updated event subscription "+args[0])
		}),
	}
	subscriptionFlags(cmd, false)
	return cmd
}()

var queuesSubscriptionDeleteCmd = sbDeleteCmd("delete <subscription-id>", "Delete an event subscription", "", cobra.ExactArgs(1),
	func(a []string) string { return "event subscription " + a[0] },
	func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("event_subscriptions/subscriptions", args[0]), nil, nil
	})

func init() {
	queueSettingsFlags(queuesCreateCmd)
	queuesCreateCmd.Flags().String("jurisdiction", "", "Restrict the queue to a jurisdiction: eu, us, or fedramp")
	queueSettingsFlags(queuesUpdateCmd)
	queuesUpdateCmd.Flags().String("name", "", "Rename the queue")
	stYes(queuesPurgeCmd)
	queuesPurgeCmd.AddCommand(queuesPurgeStatusCmd)

	queueConsumerFlags(queuesConsumerUpdateCmd, true)
	queuesConsumerUpdateCmd.Flags().Int("visibility-timeout", 0, "Milliseconds a pulled message is leased (HTTP pull consumers)")
	queuesConsumerHTTPCmd.AddCommand(queuesConsumerHTTPAddCmd, queuesConsumerHTTPRemoveCmd)
	queuesConsumerWorkerCmd.AddCommand(newQueueConsumerAddCmd(), newQueueConsumerRemoveCmd())
	queuesConsumerCmd.AddCommand(queuesConsumerListCmd, queuesConsumerGetCmd, newQueueConsumerAddCmd(), queuesConsumerUpdateCmd,
		newQueueConsumerRemoveCmd(), queuesConsumerHTTPCmd, queuesConsumerWorkerCmd)

	queuesSendCmd.Flags().String("content-type", "text", "Message content type: text or json")
	queuesSendCmd.Flags().Bool("json-body", false, "Send the body as JSON (same as --content-type json)")
	queuesSendCmd.Flags().Int("delay", 0, "Seconds before the message is delivered")
	queuesSendBatchCmd.Flags().Int("delay", 0, "Seconds before the batch is delivered")
	queuesPullCmd.Flags().Int("batch-size", 0, "Maximum messages to pull")
	queuesPullCmd.Flags().Int("visibility-timeout", 0, "Milliseconds each message stays leased")
	queuesPeekCmd.Flags().Int("batch-size", 0, "Maximum messages to show")
	queuesAckCmd.Flags().StringSlice("ack", nil, "Lease IDs to acknowledge (comma-separated)")
	queuesAckCmd.Flags().StringSlice("retry", nil, "Lease IDs to retry (comma-separated)")
	queuesAckCmd.Flags().Int("retry-delay", 0, "Seconds before retried messages are redelivered")

	queuesSubscriptionCmd.AddCommand(queuesSubscriptionListCmd, queuesSubscriptionGetCmd, queuesSubscriptionCreateCmd, queuesSubscriptionUpdateCmd, queuesSubscriptionDeleteCmd)

	queuesCmd.AddCommand(queuesListCmd, queuesGetCmd, queuesCreateCmd, queuesUpdateCmd, queuesDeleteCmd,
		queuesConsumerCmd, queuesPauseCmd, queuesResumeCmd, queuesPurgeCmd, queuesMetricsCmd,
		queuesSendCmd, queuesSendBatchCmd, queuesPullCmd, queuesPeekCmd, queuesAckCmd, queuesSubscriptionCmd)
	rootCmd.AddCommand(queuesCmd)
}
