// weft dashboard: a live terminal view of running/queued jobs, the priority
// queue, workers, and host resources — with row selection and keyboard
// actions (cancel/pause/resume/delete) dispatched through the exact same
// HTTP calls the one-shot `weft jobs action`/`weft jobs delete` commands use.
package cli

import (
	"fmt"
	"strings"
	"time"

	// aliased: this package already has its own "table" type (table.go, the
	// plain one-shot commands' fixed-width printer) — bubbles/table is a
	// different, unrelated thing (an interactive, scrollable table widget).
	btable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func cmdDashboard(args []string) error {
	fs := remoteFlagSet("dashboard")
	interval := fs.Duration("interval", 2*time.Second, "refresh interval")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	m := newDashboardModel(newClient(rf), *interval)
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// --- data shapes fetched from the API each tick ---

type dashJob struct {
	ID, Status, Priority, Profile string
	Progress                      float64
}

type dashWorker struct {
	ID, Status, Task string
}

type dashSystem struct {
	Hostname               string
	NumCPU                 int
	CPUPercent, MemPercent float64
}

type dashTask struct {
	ID, Kind, Status string
	Progress         float64
	StartedAt        *time.Time
}

type dashData struct {
	Jobs    []dashJob
	Queue   map[string]int
	Workers []dashWorker
	System  dashSystem
	Detail  []dashTask // tasks of the currently focused job, if any
	Err     error
}

// --- bubbletea messages ---

type tickMsg time.Time
type dataMsg dashData
type actionDoneMsg struct {
	line string
	err  error
}

// --- model ---

type dashboardModel struct {
	c        *client
	interval time.Duration
	tbl      btable.Model
	data     dashData
	statusLn string
	confirm  string // pending "x" (delete) confirmation, job id or ""
	quitting bool
	width    int
}

func newDashboardModel(c *client, interval time.Duration) dashboardModel {
	cols := []btable.Column{
		{Title: "ID", Width: 14},
		{Title: "STATUS", Width: 10},
		{Title: "PRIORITY", Width: 10},
		{Title: "PROFILE", Width: 14},
		{Title: "PROGRESS", Width: 9},
	}
	tbl := btable.New(btable.WithColumns(cols), btable.WithFocused(true), btable.WithHeight(10))
	return dashboardModel{c: c, interval: interval, tbl: tbl}
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), tickCmd(m.interval))
}

func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// fetchCmd polls jobs (running+queued, with progress), queue, workers,
// system, and — if a row is focused — that job's task detail (for the naive
// ETA), and reports the whole snapshot as one message.
func (m dashboardModel) fetchCmd() tea.Cmd {
	selected := m.selectedJobID()
	c := m.c
	return func() tea.Msg {
		var d dashData
		d.Jobs = fetchJobs(c, "running")
		d.Jobs = append(d.Jobs, fetchJobs(c, "queued")...)
		d.Jobs = append(d.Jobs, fetchJobs(c, "paused")...)
		d.Jobs = append(d.Jobs, fetchJobs(c, "resumed")...)
		d.Jobs = append(d.Jobs, fetchJobs(c, "uploading")...)

		var qout struct {
			Queue map[string]int `json:"queue"`
		}
		if err := c.get("/queue", &qout); err == nil {
			d.Queue = qout.Queue
		}

		var wout struct {
			Workers []struct {
				ID            string `json:"id"`
				Status        string `json:"status"`
				CurrentTaskID string `json:"current_task_id"`
			} `json:"workers"`
		}
		if err := c.get("/workers", &wout); err == nil {
			for _, w := range wout.Workers {
				d.Workers = append(d.Workers, dashWorker{ID: w.ID, Status: w.Status, Task: w.CurrentTaskID})
			}
		}

		var sout struct {
			Hostname   string  `json:"hostname"`
			NumCPU     int     `json:"num_cpu"`
			CPUPercent float64 `json:"cpu_percent"`
			MemPct     float64 `json:"mem_percent"`
		}
		if err := c.get("/system", &sout); err == nil {
			d.System = dashSystem{Hostname: sout.Hostname, NumCPU: sout.NumCPU, CPUPercent: sout.CPUPercent, MemPercent: sout.MemPct}
		}

		if selected != "" {
			d.Detail = fetchTaskDetail(c, selected)
		}
		return dataMsg(d)
	}
}

func fetchJobs(c *client, status string) []dashJob {
	var out struct {
		Jobs []struct {
			ID              string  `json:"id"`
			Status          string  `json:"status"`
			Priority        string  `json:"priority"`
			Profile         string  `json:"profile"`
			OverallProgress float64 `json:"overall_progress"`
		} `json:"jobs"`
	}
	if err := c.get("/jobs?status="+status+"&include_progress=true", &out); err != nil {
		return nil
	}
	jobs := make([]dashJob, 0, len(out.Jobs))
	for _, j := range out.Jobs {
		jobs = append(jobs, dashJob{ID: j.ID, Status: j.Status, Priority: j.Priority, Profile: j.Profile, Progress: j.OverallProgress})
	}
	return jobs
}

func fetchTaskDetail(c *client, jobID string) []dashTask {
	var out struct {
		Tasks []struct {
			ID        string     `json:"id"`
			Kind      string     `json:"kind"`
			Status    string     `json:"status"`
			Progress  float64    `json:"progress_percent"`
			StartedAt *time.Time `json:"started_at,omitempty"`
		} `json:"tasks"`
	}
	if err := c.get("/jobs/"+jobID, &out); err != nil {
		return nil
	}
	tasks := make([]dashTask, 0, len(out.Tasks))
	for _, t := range out.Tasks {
		tasks = append(tasks, dashTask{ID: t.ID, Kind: t.Kind, Status: t.Status, Progress: t.Progress, StartedAt: t.StartedAt})
	}
	return tasks
}

func (m dashboardModel) selectedJobID() string {
	row := m.tbl.SelectedRow()
	if len(row) == 0 {
		return ""
	}
	return row[0]
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.fetchCmd(), tickCmd(m.interval))

	case dataMsg:
		m.data = dashData(msg)
		m.tbl.SetRows(jobsToRows(m.data.Jobs))
		return m, nil

	case actionDoneMsg:
		if msg.err != nil {
			m.statusLn = "error: " + msg.err.Error()
		} else {
			m.statusLn = msg.line
		}
		return m, m.fetchCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	var cmd tea.Cmd
	m.tbl, cmd = m.tbl.Update(msg)
	return m, cmd
}

func (m dashboardModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// a pending delete confirmation swallows the next keypress: y confirms,
	// anything else cancels.
	if m.confirm != "" {
		id := m.confirm
		m.confirm = ""
		if msg.String() == "y" {
			return m, m.actionCmd(id, "delete")
		}
		m.statusLn = "delete cancelled"
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "enter":
		return m, m.fetchCmd()
	case "c":
		if id := m.selectedJobID(); id != "" {
			return m, m.actionCmd(id, "cancel")
		}
	case "p":
		if id := m.selectedJobID(); id != "" {
			return m, m.actionCmd(id, "pause")
		}
	case "r":
		if id := m.selectedJobID(); id != "" {
			return m, m.actionCmd(id, "resume")
		}
	case "x":
		if id := m.selectedJobID(); id != "" {
			m.confirm = id
			m.statusLn = fmt.Sprintf("delete %s? (y/N)", id)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.tbl, cmd = m.tbl.Update(msg)
	return m, cmd
}

// actionCmd dispatches a job action through the same HTTP calls
// jobsAction/jobsDelete use — the dashboard is a thin polling+keyboard
// wrapper around already-tested command logic, not a reimplementation.
func (m dashboardModel) actionCmd(id, action string) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		if action == "delete" {
			var out struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			if err := c.del("/jobs/"+id, &out); err != nil {
				return actionDoneMsg{err: err}
			}
			return actionDoneMsg{line: fmt.Sprintf("job %s deleted", id)}
		}
		var out struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := c.post("/jobs/"+id+"/"+action, map[string]any{}, &out); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{line: fmt.Sprintf("job %s -> %s", out.ID, out.Status)}
	}
}

func jobsToRows(jobs []dashJob) []btable.Row {
	rows := make([]btable.Row, 0, len(jobs))
	for _, j := range jobs {
		rows = append(rows, btable.Row{j.ID, j.Status, j.Priority, j.Profile, fmt.Sprintf("%.0f%%", j.Progress)})
	}
	return rows
}

var (
	dashTitleStyle = lipgloss.NewStyle().Bold(true)
	dashDimStyle   = lipgloss.NewStyle().Faint(true)
)

func (m dashboardModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString(dashTitleStyle.Render("weft dashboard") + dashDimStyle.Render("  (↑/↓ select · c cancel · p pause · r resume · x delete · enter refresh · q quit)") + "\n\n")
	if len(m.data.Jobs) == 0 {
		b.WriteString("no active jobs\n\n")
	} else {
		b.WriteString(m.tbl.View() + "\n\n")
	}
	b.WriteString(renderQueueLine(m.data.Queue) + "\n")
	b.WriteString(renderWorkersLine(m.data.Workers) + "\n")
	b.WriteString(renderSystemLine(m.data.System) + "\n")
	if detail := renderDetail(m.data.Detail); detail != "" {
		b.WriteString("\n" + detail)
	}
	if m.statusLn != "" {
		b.WriteString("\n" + dashDimStyle.Render(m.statusLn) + "\n")
	}
	return b.String()
}

// renderQueueLine, renderWorkersLine, renderSystemLine, renderDetail, and
// computeETA are pure functions (no I/O, no bubbletea types) so they're
// unit-testable without a terminal — see dashboard_test.go.

func renderQueueLine(queue map[string]int) string {
	if len(queue) == 0 {
		return "queue: empty"
	}
	order := []string{"emergency", "high", "normal", "low", "background"}
	parts := make([]string, 0, len(order))
	for _, p := range order {
		if n, ok := queue[p]; ok && n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", p, n))
		}
	}
	if len(parts) == 0 {
		return "queue: empty"
	}
	return "queue: " + strings.Join(parts, " ")
}

func renderWorkersLine(workers []dashWorker) string {
	if len(workers) == 0 {
		return "workers: none"
	}
	busy := 0
	for _, w := range workers {
		if w.Status == "busy" {
			busy++
		}
	}
	return fmt.Sprintf("workers: %d/%d busy", busy, len(workers))
}

func renderSystemLine(s dashSystem) string {
	if s.Hostname == "" {
		return "system: (unavailable)"
	}
	return fmt.Sprintf("host: %s  cpu: %d cores %.1f%%  mem: %.1f%%", s.Hostname, s.NumCPU, s.CPUPercent, s.MemPercent)
}

func renderDetail(tasks []dashTask) string {
	if len(tasks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(dashTitleStyle.Render("selected job tasks") + "\n")
	now := time.Now()
	for _, t := range tasks {
		eta := computeETA(t.StartedAt, t.Progress, now)
		b.WriteString(fmt.Sprintf("  %-10s %-14s %-10s %5.1f%%  eta %s\n", t.ID, t.Kind, t.Status, t.Progress, eta))
	}
	return b.String()
}

// computeETA is a naive linear projection from elapsed time and percent
// complete: elapsed/(progress/100) - elapsed. Deliberately simple — no
// smoothing, no per-task throughput history — since it's a rough "how much
// longer" hint, not a scheduling input.
func computeETA(startedAt *time.Time, progress float64, now time.Time) string {
	if startedAt == nil || startedAt.IsZero() {
		return "-"
	}
	if progress <= 0 {
		return "unknown"
	}
	if progress >= 100 {
		return "done"
	}
	elapsed := now.Sub(*startedAt)
	if elapsed <= 0 {
		return "unknown"
	}
	total := elapsed.Seconds() / (progress / 100)
	remaining := time.Duration(total-elapsed.Seconds()) * time.Second
	if remaining < 0 {
		remaining = 0
	}
	return remaining.Round(time.Second).String()
}
