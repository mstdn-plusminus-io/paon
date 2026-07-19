package api

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

type fakeAsynqInspectorClient struct {
	queues         []string
	queueInfos     map[string]*asynq.QueueInfo
	history        map[string][]*asynq.DailyStats
	servers        []*asynq.ServerInfo
	tasks          map[string]map[string][]*asynq.TaskInfo
	queueErr       error
	queueInfoErr   error
	historyErr     error
	serversErr     error
	listErr        map[string]error
	queueInfoCalls map[string]int
	listCalls      map[string]int
	listRequests   map[string][]fakeAsynqListRequest
	closeCalls     int
}

type fakeAsynqListRequest struct {
	Page int
	Size int
}

func (f *fakeAsynqInspectorClient) Close() error {
	f.closeCalls++
	return nil
}

func (f *fakeAsynqInspectorClient) Queues() ([]string, error) {
	return append([]string(nil), f.queues...), f.queueErr
}

func (f *fakeAsynqInspectorClient) GetQueueInfo(queue string) (*asynq.QueueInfo, error) {
	if f.queueInfoCalls == nil {
		f.queueInfoCalls = make(map[string]int)
	}
	f.queueInfoCalls[queue]++
	if f.queueInfoErr != nil {
		return nil, f.queueInfoErr
	}
	info, ok := f.queueInfos[queue]
	if !ok {
		return nil, asynq.ErrQueueNotFound
	}
	return info, nil
}

func (f *fakeAsynqInspectorClient) History(queue string, days int) ([]*asynq.DailyStats, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return append([]*asynq.DailyStats(nil), f.history[queue]...), nil
}

func (f *fakeAsynqInspectorClient) Servers() ([]*asynq.ServerInfo, error) {
	return append([]*asynq.ServerInfo(nil), f.servers...), f.serversErr
}

func fakeAsynqListRequestFromOptions(opts []asynq.ListOption) fakeAsynqListRequest {
	request := fakeAsynqListRequest{Page: 1, Size: 30}
	for _, option := range opts {
		value := reflect.ValueOf(option)
		if !value.IsValid() || value.Kind() != reflect.Int {
			continue
		}
		switch reflect.TypeOf(option).Name() {
		case "pageNumOpt":
			request.Page = int(value.Int())
		case "pageSizeOpt":
			request.Size = int(value.Int())
		}
	}
	if request.Page < 1 {
		request.Page = 1
	}
	if request.Size < 0 {
		request.Size = 0
	}
	return request
}

func (f *fakeAsynqInspectorClient) list(state string, queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	if f.listCalls == nil {
		f.listCalls = make(map[string]int)
	}
	key := state + "\x00" + queue
	f.listCalls[key]++
	if f.listRequests == nil {
		f.listRequests = make(map[string][]fakeAsynqListRequest)
	}
	request := fakeAsynqListRequestFromOptions(opts)
	f.listRequests[key] = append(f.listRequests[key], request)
	if err := f.listErr[state]; err != nil {
		return nil, err
	}
	tasks := f.tasks[state][queue]
	start := (request.Page - 1) * request.Size
	if request.Size == 0 || start >= len(tasks) {
		return []*asynq.TaskInfo{}, nil
	}
	end := min(start+request.Size, len(tasks))
	return append([]*asynq.TaskInfo(nil), tasks[start:end]...), nil
}

func (f *fakeAsynqInspectorClient) ListPendingTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("pending", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListActiveTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("active", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListRetryTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("retry", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListScheduledTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("scheduled", queue, opts...)
}

func (f *fakeAsynqInspectorClient) ListArchivedTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("archived", queue, opts...)
}

func asynqIssueCodesForQueue(issues []asynqDashboardIssue, queue string) []string {
	var out []string
	for _, issue := range issues {
		if issue.Queue == queue {
			out = append(out, issue.Code)
		}
	}
	return out
}

func TestCollectAsynqDashboardAggregatesAndOrdersOperationalState(t *testing.T) {
	now := time.Now().UTC()
	defaultQueue := "tenant:default"
	pullQueue := "tenant:pull"
	zetaQueue := "tenant:zeta"
	inspector := &fakeAsynqInspectorClient{
		queues: []string{zetaQueue, "foreign:ignored", pullQueue, defaultQueue},
		queueInfos: map[string]*asynq.QueueInfo{
			defaultQueue: {
				Queue: defaultQueue, ProcessedTotal: 100, FailedTotal: 5,
				Pending: 2, Active: 1, Retry: 3, Scheduled: 4, Archived: 5,
				Latency: 90 * time.Second, MemoryUsage: 4096, Paused: true,
			},
			pullQueue: {
				Queue: pullQueue, ProcessedTotal: 20, FailedTotal: 1, Pending: 7,
			},
			zetaQueue: {Queue: zetaQueue},
		},
		history: map[string][]*asynq.DailyStats{
			defaultQueue: {
				{Queue: defaultQueue, Date: now.AddDate(0, 0, -1), Processed: 4},
				{Queue: defaultQueue, Date: now, Processed: 10, Failed: 2},
			},
			pullQueue: {{Queue: pullQueue, Date: now, Processed: 5, Failed: 1}},
		},
		servers: []*asynq.ServerInfo{
			{
				ID: "server-z", Host: "z-worker", PID: 20, Status: "active", Concurrency: 1,
				Queues: map[string]int{defaultQueue: 1},
				ActiveWorkers: []*asynq.WorkerInfo{{
					TaskID: "deadline-task", TaskType: "deadline", Queue: defaultQueue,
					Started: now.Add(-2 * time.Minute), Deadline: now.Add(-time.Minute),
				}},
			},
			{
				ID: "server-a", Host: "a-worker", PID: 10, Status: "active", Concurrency: 4,
				Queues: map[string]int{"tenant:ingress": 4}, ActiveWorkers: []*asynq.WorkerInfo{},
			},
			{ID: "foreign", Host: "foreign-worker", Status: "active", Queues: map[string]int{"foreign:ignored": 1}},
		},
		tasks: map[string]map[string][]*asynq.TaskInfo{
			"active": {
				defaultQueue: {{ID: "orphan-task", Queue: defaultQueue, Type: "orphan", State: asynq.TaskStateActive, IsOrphaned: true}},
			},
		},
		listErr: make(map[string]error),
	}
	server := &Server{cfg: config.Config{RedisNamespace: "tenant:"}, asynqInspector: inspector}

	data, err := server.collectAsynqDashboard("en")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := data.Snapshot
	if !snapshot.Available || snapshot.Error != "" {
		t.Fatalf("availability = %v error = %q", snapshot.Available, snapshot.Error)
	}
	wantSummary := asynqDashboardSummary{ProcessedTotal: 120, FailedTotal: 6, Active: 1, Pending: 9, Retry: 3, Scheduled: 4, Archived: 5}
	if snapshot.Summary != wantSummary {
		t.Fatalf("summary = %#v, want %#v", snapshot.Summary, wantSummary)
	}

	var queueNames []string
	for _, queue := range snapshot.Queues {
		queueNames = append(queueNames, queue.Name)
	}
	wantQueues := []string{"tenant:default", "tenant:ingress", "tenant:mailers", "tenant:pull", "tenant:push", "tenant:zeta"}
	if !reflect.DeepEqual(queueNames, wantQueues) {
		t.Fatalf("queue order = %#v, want %#v", queueNames, wantQueues)
	}
	if got := snapshot.Queues[len(snapshot.Queues)-1].DisplayName; got != "zeta" {
		t.Fatalf("discovered unknown queue display name = %q", got)
	}
	if got := snapshot.Queues[0]; got.ActiveConsumers != 1 || !got.ConsumerAvailable || got.ConsumerCapacity != 1 || got.FailedTotal != 5 {
		t.Fatalf("default queue consumer/failure metrics = %#v", got)
	}
	if len(snapshot.Servers) != 2 || snapshot.Servers[0].Host != "a-worker" || snapshot.Servers[1].Host != "z-worker" {
		t.Fatalf("server order/filtering = %#v", snapshot.Servers)
	}
	if snapshot.Servers[1].Utilization != 100 || snapshot.Servers[1].Workers[0].Elapsed == "" {
		t.Fatalf("server utilization/worker elapsed = %#v", snapshot.Servers[1])
	}

	wantDefaultIssues := []string{"archived", "deadline_exceeded", "orphaned", "paused", "retry", "saturated"}
	if got := asynqIssueCodesForQueue(snapshot.Issues, "default"); !reflect.DeepEqual(got, wantDefaultIssues) {
		t.Fatalf("default issues = %#v, want %#v", got, wantDefaultIssues)
	}
	if got := asynqIssueCodesForQueue(snapshot.Issues, "pull"); !reflect.DeepEqual(got, []string{"no_consumer"}) {
		t.Fatalf("pull issues = %#v", got)
	}
	if len(snapshot.History) != asynqHistoryDays {
		t.Fatalf("history length = %d, want %d", len(snapshot.History), asynqHistoryDays)
	}
	var currentDay *asynqHistoryView
	for i := range snapshot.History {
		if snapshot.History[i].Date == now.Format("2006-01-02") {
			currentDay = &snapshot.History[i]
			break
		}
	}
	if currentDay == nil || currentDay.Processed != 15 || currentDay.Failed != 3 || currentDay.Succeeded != 12 {
		t.Fatalf("aggregated current-day history = %#v", currentDay)
	}
	if inspector.queueInfoCalls["foreign:ignored"] != 0 {
		t.Fatal("foreign namespace queue was inspected")
	}
}

func TestCollectAsynqDashboardWithoutNamespaceIgnoresUnconfiguredQueues(t *testing.T) {
	inspector := &fakeAsynqInspectorClient{
		queues: []string{"default", "another-app"},
		queueInfos: map[string]*asynq.QueueInfo{
			"default":     {Queue: "default", Pending: 2},
			"another-app": {Queue: "another-app", Pending: 999},
		},
		history: map[string][]*asynq.DailyStats{},
		servers: []*asynq.ServerInfo{},
		tasks:   map[string]map[string][]*asynq.TaskInfo{},
		listErr: map[string]error{},
	}
	server := &Server{asynqInspector: inspector}
	data, err := server.collectAsynqDashboard("en")
	if err != nil {
		t.Fatal(err)
	}
	if data.Snapshot.Summary.Pending != 2 {
		t.Fatalf("pending = %d, want only Paon queue count 2", data.Snapshot.Summary.Pending)
	}
	if inspector.queueInfoCalls["another-app"] != 0 {
		t.Fatal("an unconfigured queue was inspected without an ownership namespace")
	}
}

func TestCollectAsynqDashboardPropagatesInspectorFailuresAndStatsUses503(t *testing.T) {
	redisFailure := errors.New("dial redis://secret@redis.internal:6379: refused")
	cases := []struct {
		name      string
		inspector *fakeAsynqInspectorClient
	}{
		{"queues", &fakeAsynqInspectorClient{queueErr: redisFailure}},
		{"queue info", &fakeAsynqInspectorClient{queues: []string{"default"}, queueInfos: map[string]*asynq.QueueInfo{}, queueInfoErr: redisFailure}},
		{"servers", &fakeAsynqInspectorClient{serversErr: redisFailure}},
		{"active tasks", &fakeAsynqInspectorClient{
			queues: []string{"default"}, queueInfos: map[string]*asynq.QueueInfo{"default": {Queue: "default", Active: 1}},
			tasks: map[string]map[string][]*asynq.TaskInfo{}, listErr: map[string]error{"active": redisFailure},
		}},
		{"history", &fakeAsynqInspectorClient{
			queues: []string{"default"}, queueInfos: map[string]*asynq.QueueInfo{"default": {Queue: "default"}}, historyErr: redisFailure,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{asynqInspector: tc.inspector}
			if _, err := server.collectAsynqDashboard("en"); err == nil {
				t.Fatal("collectAsynqDashboard succeeded despite inspector failure")
			}
			snapshot := asynqUnavailableSnapshot("en", redisFailure)
			if snapshot.Available || snapshot.Error == "" || strings.Contains(snapshot.Error, "redis.internal") || len(snapshot.Queues) != 0 {
				t.Fatalf("unsafe unavailable snapshot = %#v", snapshot)
			}
		})
	}

	src, err := os.ReadFile("admin_asynq.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "sidekiqStats", "http.StatusServiceUnavailable") {
		t.Fatal("sidekiqStats must return HTTP 503 when Asynq inspection fails")
	}
}

func TestAsynqTaskDetailsShowCompleteRawPayloadAndErrorWithHTMLEscaping(t *testing.T) {
	payload := []byte(`{"token":"ADMIN-TOKEN","body":"</pre><script>alert('payload')</script>","large":"` + strings.Repeat("x", 70*1024) + `"}`)
	lastError := `delivery failed: privateKey=ERROR-SECRET </pre><script>alert('error')</script> ` + strings.Repeat("e", 2_000)
	inspector := &fakeAsynqInspectorClient{
		tasks: map[string]map[string][]*asynq.TaskInfo{
			"pending": {"default": {{ID: "raw-task", Queue: "default", Type: "raw", Payload: payload, LastErr: lastError}}},
		},
		listErr: map[string]error{},
	}
	server := &Server{asynqInspector: inspector}
	data := &asynqDashboardData{
		QueueInfos:  map[string]*asynq.QueueInfo{"default": {Queue: "default", Pending: 1}},
		ActiveTasks: map[string][]*asynq.TaskInfo{}, WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
	}
	page, err := server.asynqTaskPage(data, "pending", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(page.Tasks))
	}
	task := page.Tasks[0]
	if task.RawPayload != string(payload) {
		t.Fatalf("raw payload changed: got %d bytes, want %d", len(task.RawPayload), len(payload))
	}
	if task.LastError != lastError {
		t.Fatalf("last error changed: got %d bytes, want %d", len(task.LastError), len(lastError))
	}

	rows := asynqTaskRowsHTML(&page, "en")
	for _, want := range []string{"ADMIN-TOKEN", "ERROR-SECRET", "Raw payload", "&lt;/pre&gt;&lt;script&gt;alert(&#39;payload&#39;)&lt;/script&gt;", "&lt;/pre&gt;&lt;script&gt;alert(&#39;error&#39;)&lt;/script&gt;"} {
		if !strings.Contains(rows, want) {
			t.Fatalf("task HTML missing %q", want)
		}
	}
	for _, unwanted := range []string{"<script>", "[REDACTED]", "payload omitted", "error detail redacted"} {
		if strings.Contains(rows, unwanted) {
			t.Fatalf("task HTML unexpectedly contains %q", unwanted)
		}
	}
}

func makeAsynqTaskInfosInReverseLexicalOrder(queue string, prefix string, count int) []*asynq.TaskInfo {
	tasks := make([]*asynq.TaskInfo, 0, count)
	for i := 0; i < count; i++ {
		tasks = append(tasks, &asynq.TaskInfo{ID: fmt.Sprintf("%s%03d", prefix, count-1-i), Queue: queue, Type: "test"})
	}
	return tasks
}

func TestAsynqTaskPagePaginatesGloballyAcrossQueues(t *testing.T) {
	inspector := &fakeAsynqInspectorClient{
		tasks: map[string]map[string][]*asynq.TaskInfo{
			"pending": {
				"a": makeAsynqTaskInfosInReverseLexicalOrder("a", "a", 60),
				"b": makeAsynqTaskInfosInReverseLexicalOrder("b", "b", 60),
			},
		},
		listErr: make(map[string]error),
	}
	server := &Server{asynqInspector: inspector}
	data := &asynqDashboardData{
		QueueInfos:  map[string]*asynq.QueueInfo{"b": {Queue: "b", Pending: 60}, "a": {Queue: "a", Pending: 60}},
		ActiveTasks: map[string][]*asynq.TaskInfo{}, WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
	}
	first, err := server.asynqTaskPage(data, "pending", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 120 || len(first.Tasks) != 50 || first.Tasks[0].ID != "a059" || first.Tasks[49].ID != "a010" || first.HasPrevious || !first.HasNext {
		t.Fatalf("first global page = %#v", first)
	}
	for _, key := range []string{"pending\x00a", "pending\x00b"} {
		if got := inspector.listRequests[key]; !reflect.DeepEqual(got, []fakeAsynqListRequest{{Page: 1, Size: 50}}) {
			t.Fatalf("first-page Inspector request %q = %#v", key, got)
		}
	}
	second, err := server.asynqTaskPage(data, "pending", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 120 || len(second.Tasks) != 50 || second.Tasks[0].ID != "a009" || second.Tasks[9].ID != "a000" || second.Tasks[10].ID != "b059" || second.Tasks[49].ID != "b020" || !second.HasPrevious || !second.HasNext {
		t.Fatalf("second global page = %#v", second)
	}
	firstIDs := make(map[string]struct{}, len(first.Tasks))
	for _, task := range first.Tasks {
		firstIDs[task.Queue+"\x00"+task.ID] = struct{}{}
	}
	for _, task := range second.Tasks {
		if _, ok := firstIDs[task.Queue+"\x00"+task.ID]; ok {
			t.Fatalf("task %s/%s appeared on both pages", task.Queue, task.ID)
		}
	}
	for _, key := range []string{"pending\x00a", "pending\x00b"} {
		want := []fakeAsynqListRequest{{Page: 1, Size: 50}, {Page: 1, Size: 60}}
		if got := inspector.listRequests[key]; !reflect.DeepEqual(got, want) {
			t.Fatalf("global Inspector requests %q = %#v, want %#v", key, got, want)
		}
	}
}

func TestAsynqArchivedTaskPageUsesGlobalNewestOrdering(t *testing.T) {
	base := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	archived := map[string][]*asynq.TaskInfo{"a": {}, "b": {}}
	for position := 0; position < 75; position++ {
		queue := "a"
		if position%5 == 0 {
			queue = "b"
		}
		// Inspector returns archived tasks oldest-to-newest. IDs deliberately do
		// not follow time order so this catches accidental lexical sorting.
		archived[queue] = append(archived[queue], &asynq.TaskInfo{
			ID:           fmt.Sprintf("id-%03d", (position*37)%101),
			Queue:        queue,
			LastFailedAt: base.Add(time.Duration(position) * time.Minute),
		})
	}
	inspector := &fakeAsynqInspectorClient{
		tasks:   map[string]map[string][]*asynq.TaskInfo{"archived": archived},
		listErr: make(map[string]error),
	}
	server := &Server{asynqInspector: inspector}
	data := &asynqDashboardData{
		QueueInfos:  map[string]*asynq.QueueInfo{"a": {Queue: "a", Archived: 60}, "b": {Queue: "b", Archived: 15}},
		ActiveTasks: map[string][]*asynq.TaskInfo{}, WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
	}
	first, err := server.asynqTaskPage(data, "archived", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 75 || len(first.Tasks) != 50 || first.Tasks[0].ID != "id-011" || first.Tasks[49].ID != "id-016" || !first.HasNext {
		t.Fatalf("newest archived page = %#v", first.Tasks)
	}
	for i := 1; i < len(first.Tasks); i++ {
		if first.Tasks[i].LastFailedAt.After(first.Tasks[i-1].LastFailedAt) {
			t.Fatalf("archived tasks are not newest-first at %d: %#v", i, first.Tasks[i-1:i+1])
		}
	}
	second, err := server.asynqTaskPage(data, "archived", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Tasks) != 25 || second.Tasks[0].ID != "id-080" || second.Tasks[24].ID != "id-000" || !second.HasPrevious || second.HasNext {
		t.Fatalf("second archived page = %#v", second.Tasks)
	}
	firstIDs := make(map[string]struct{}, len(first.Tasks))
	for _, task := range first.Tasks {
		firstIDs[task.Queue+"\x00"+task.ID] = struct{}{}
	}
	for _, task := range second.Tasks {
		if _, ok := firstIDs[task.Queue+"\x00"+task.ID]; ok {
			t.Fatalf("archived task %s/%s appeared on both pages", task.Queue, task.ID)
		}
	}
	if got, want := inspector.listRequests["archived\x00a"], []fakeAsynqListRequest{{Page: 1, Size: 50}, {Page: 2, Size: 50}, {Page: 1, Size: 60}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archived tail-window requests for queue a = %#v, want %#v", got, want)
	}
	if got, want := inspector.listRequests["archived\x00b"], []fakeAsynqListRequest{{Page: 1, Size: 15}, {Page: 1, Size: 15}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archived tail-window requests for queue b = %#v, want %#v", got, want)
	}
}

func TestAsynqTaskPageSupportsEveryReadOnlyStateAndRejectsUnknownQueue(t *testing.T) {
	states := []string{"active", "pending", "retry", "scheduled", "archived"}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			info := &asynq.QueueInfo{Queue: "tenant:custom"}
			switch state {
			case "active":
				info.Active = 1
			case "pending":
				info.Pending = 1
			case "retry":
				info.Retry = 1
			case "scheduled":
				info.Scheduled = 1
			case "archived":
				info.Archived = 1
			}
			inspector := &fakeAsynqInspectorClient{
				tasks: map[string]map[string][]*asynq.TaskInfo{
					state: {"tenant:custom": {{ID: state + "-task", Queue: "tenant:custom", Type: "test"}}},
				},
				listErr: make(map[string]error),
			}
			server := &Server{cfg: config.Config{RedisNamespace: "tenant"}, asynqInspector: inspector}
			data := &asynqDashboardData{
				QueueInfos: map[string]*asynq.QueueInfo{"tenant:custom": info}, ActiveTasks: map[string][]*asynq.TaskInfo{},
				WorkerByTask: map[string]*asynq.WorkerInfo{}, ServerByTask: map[string]*asynq.ServerInfo{},
			}
			page, err := server.asynqTaskPage(data, state, "custom", 1)
			if err != nil {
				t.Fatal(err)
			}
			if page.Queue != "tenant:custom" || len(page.Tasks) != 1 || page.Tasks[0].ID != state+"-task" || page.Tasks[0].State != state {
				t.Fatalf("%s page = %#v", state, page)
			}
			if inspector.listCalls[state+"\x00tenant:custom"] != 1 {
				t.Fatalf("%s list calls = %#v", state, inspector.listCalls)
			}

			callKey := state + "\x00tenant:custom"
			before := inspector.listCalls[callKey]
			if _, err := server.asynqTaskPage(data, state, "missing", 1); !errors.Is(err, errUnknownAsynqQueue) {
				t.Fatalf("unknown queue error = %v", err)
			}
			if inspector.listCalls[callKey] != before {
				t.Fatal("unknown queue reached Inspector")
			}
		})
	}
}
