package tui

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/reasons"
)

// Asking why a task stopped (#1087). Most chats opened on a stopped task
// asked one question, typed by hand, and an agent then spent its first tool
// calls finding the failure the failure card already showed. `Q` opens the
// chat with that question already sent, carrying the card's facts; the
// chat's first answer is then shown on the card as the diagnosis, and a task
// that stops again at the same step for the same reason shows it before a
// new chat is started — and hands it to that chat.
//
// Nothing here is stored on the task. A diagnosis chat is an ordinary chat
// opened on the task (task 119), told apart by its title, which names the
// step and the reason it was asked about. A renamed chat stops being one,
// which is the human's call to make.

// actionDiagnose is the card's and the registry's name for `Q`: not a §6
// action of its own but `chat` with the question already asked, so it is
// offered exactly where a stopped task offers `chat`.
const actionDiagnose = "diagnose"

// diagnoseOpeningLines is how many lines of a diagnosis the card quotes.
const diagnoseOpeningLines = 4

// diagnoseTitle names a diagnosis chat by what it was asked about.
func diagnoseTitle(stepNum int, stepName, reason string) string {
	step := "step " + strconv.Itoa(stepNum)
	if stepName != "" {
		step += " " + stepName
	}
	return "why " + step + " stopped · " + reason
}

var diagnoseTitlePattern = regexp.MustCompile(`^why step (\d+)\b.* stopped · ([a-z0-9_]+)$`)

// parseDiagnoseTitle reads back what diagnoseTitle wrote.
func parseDiagnoseTitle(title string) (stepNum int, reason string, ok bool) {
	m := diagnoseTitlePattern.FindStringSubmatch(title)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, m[2], true
}

// cardStepNum is the 1-based step a card is about.
func (c failureCard) cardStepNum() int {
	if c.hasRun && !c.stepless {
		return c.run.StepIndex + 1
	}
	return c.stepNum
}

func (c failureCard) cardStepName() string {
	if c.hasRun && !c.stepless {
		return stepLabel(c.run)
	}
	return c.stepName
}

// offersDiagnose is whether `Q` does anything for this target: a stopped
// task — blocked or aborted — that offers a chat, or holds one open, which
// the key then reopens as `T` does. Never under a bulk selection.
func (t taskActions) offersDiagnose() bool {
	return (t.state == stateBlocked || t.state == stateAborted) && t.offersChat()
}

// diagnosisChat is the newest closed diagnosis chat on the task for the
// card's step and reason, and false when there is none. A chat still open
// is not one yet: its answer may still be coming, and it holds the task.
func diagnosisChat(chats []apiclient.Chat, c failureCard) (apiclient.Chat, bool) {
	if c.reason == "" {
		return apiclient.Chat{}, false
	}
	var best apiclient.Chat
	for _, ch := range chats {
		if ch.State != string(chatstate.Closed) {
			continue
		}
		step, reason, ok := parseDiagnoseTitle(ch.Title)
		if !ok || step != c.cardStepNum() || reason != c.reason {
			continue
		}
		if ch.ID > best.ID {
			best = ch
		}
	}
	return best, best.ID != 0
}

// firstAnswer is the opening of a chat's first answer: its first non-empty
// lines, at most n of them.
func firstAnswer(turns []apiclient.ChatTurn, n int) []string {
	sort.Slice(turns, func(i, j int) bool { return turns[i].Seq < turns[j].Seq })
	for _, turn := range turns {
		if strings.TrimSpace(turn.ResultText) == "" {
			continue
		}
		var out []string
		for _, line := range strings.Split(turn.ResultText, "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if out = append(out, line); len(out) == n {
				break
			}
		}
		return out
	}
	return nil
}

// diagnoseMessage is the question `Q` sends, with what the card shows, so
// the agent starts from the failure rather than looking for it. earlier is
// a previous diagnosis of the same stop, which the agent is told to check
// first: three of task 968's six chats rediscovered the same cause.
func diagnoseMessage(task apiclient.TaskDetail, c failureCard, ev cardEvidence,
	earlier apiclient.Chat, earlierAnswer []string,
) string {
	var b strings.Builder
	b.WriteString("Why did this task stop? Find the cause and say what would fix it. " +
		"Diagnose only: do not change any files yet.\n\n")
	where := ""
	if n := c.cardStepNum(); n > 0 {
		where = fmt.Sprintf(" at step %d", n)
		if name := c.cardStepName(); name != "" {
			where += " " + strconv.Quote(name)
		}
	}
	if c.hasRun && !c.stepless {
		where += fmt.Sprintf(", attempt %d", c.run.Attempt)
		if c.run.Iteration > 0 {
			where += fmt.Sprintf(" (loop iteration %d)", c.run.Iteration)
		}
	}
	reason := "it stopped"
	if c.reason != "" {
		reason = reasons.Explain(c.reason).Title + " (" + c.reason + ")"
	}
	fmt.Fprintf(&b, "Task #%d %s%s: %s.\n", task.ID, stateWords(task.State), where, reason)
	if ev.exit != "" {
		b.WriteString(strings.ToUpper(ev.exit[:1]) + ev.exit[1:] + ".\n")
	}
	if c.detail != "" {
		b.WriteString("The daemon said: " + oneLine(c.detail) + "\n")
	}
	if ev.script != "" {
		b.WriteString("\nThe command that failed:\n")
		for _, line := range strings.Split(ev.script, "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
	if len(ev.lines) > 0 {
		b.WriteString("\nWhat it printed (selected lines; ⋯ marks lines left out):\n")
		for _, l := range ev.lines {
			switch {
			case l.gap:
				b.WriteString("    ⋯\n")
			case l.stderr:
				b.WriteString("    [stderr] " + l.text + "\n")
			default:
				b.WriteString("    " + l.text + "\n")
			}
		}
	}
	if c.status != "" {
		b.WriteString("\nThe step's last status before the failure, which may name a stage it had already passed: " +
			strconv.Quote(c.status) + "\n")
	}
	if earlier.ID != 0 && len(earlierAnswer) > 0 {
		fmt.Fprintf(&b, "\nIt stopped here for the same reason before. An earlier chat (#%d) concluded:\n", earlier.ID)
		for _, line := range earlierAnswer {
			b.WriteString("    " + line + "\n")
		}
		b.WriteString("Check whether that still holds before looking elsewhere.\n")
	}
	return b.String()
}

// diagnoseCmd is `Q`: it reads what the failure card would show — the task,
// the failing attempt's transcript and any earlier diagnosis of the same
// stop — and opens a chat on the task with the question already sent. It
// fetches for itself, so the board's key and the workspace's are one path.
// A chat already open on the task is reopened instead, as `T` does.
func diagnoseCmd(client *apiclient.Client, t taskActions, bar *actionBar) tea.Cmd {
	if !t.offersDiagnose() {
		return nil
	}
	if t.openChatID != 0 {
		id, pid := t.openChatID, t.projectID
		return func() tea.Msg { return openChatMsg{id: id, projectID: pid} }
	}
	if client == nil {
		bar.setStatus("not connected", true)
		return nil
	}
	bar.setStatus("asking an agent why it stopped…", false)
	id := t.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		task, err := client.GetTask(ctx, id)
		if err != nil {
			return taskChatOpenedMsg{taskID: id, err: err}
		}
		c := deriveFailureCard(task, laneBlame{}, false)
		var ev cardEvidence
		if c.wantsEvidence() {
			recs, _, err := client.Transcript(ctx, id, c.run.ID,
				apiclient.TranscriptOptions{Tail: apiclient.DefaultTailBytes})
			if err == nil {
				ev = readEvidence(c, recs, evidenceLines)
			}
		}
		var earlier apiclient.Chat
		var answer []string
		if chats, err := client.ListChats(ctx, apiclient.ListChatsOptions{
			TaskID: id, Archived: apiclient.ArchivedAll,
		}); err == nil {
			if ch, ok := diagnosisChat(chats, c); ok {
				if _, turns, err := client.GetChat(ctx, ch.ID); err == nil {
					earlier, answer = ch, firstAnswer(turns, diagnoseOpeningLines)
				}
			}
		}
		chat, err := client.OpenTaskChat(context.Background(), id, apiclient.OpenTaskChatRequest{
			Title:   diagnoseTitle(c.cardStepNum(), c.cardStepName(), c.reason),
			Message: diagnoseMessage(task, c, ev, earlier, answer),
		})
		if err != nil {
			if chatID, locked := apiclient.TaskLockedByChat(err); locked {
				return taskChatOpenedMsg{taskID: id, chatID: chatID}
			}
			return taskChatOpenedMsg{taskID: id, err: err}
		}
		return taskChatOpenedMsg{taskID: id, chatID: chat.ID}
	}
}

// taskDiagnosis is the card's one read of a diagnosis chat: the chat and
// its first answer's opening, keyed by the chat.
type taskDiagnosis struct {
	chatID   int64
	fetching bool
	chat     apiclient.Chat
	answer   []string
	err      error
}

type taskDiagnosisMsg struct {
	taskID int64
	chat   apiclient.Chat
	answer []string
	err    error
}

// syncDiagnosis fetches the diagnosis chat the card would show, once per
// chat: when the chat list names one for the card's step and reason that is
// not the one already read. It runs after every message, as
// syncFailureEvidence does.
func (t *taskView) syncDiagnosis() tea.Cmd {
	if !t.showsFailureCard() {
		return nil
	}
	ch, ok := diagnosisChat(t.chats, t.failureCard())
	if !ok || t.diagnosis.chatID == ch.ID {
		return nil
	}
	client, taskID := t.detail.client, t.detail.taskID
	t.diagnosis = taskDiagnosis{chatID: ch.ID, fetching: client != nil, chat: ch}
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		_, turns, err := client.GetChat(ctx, ch.ID)
		return taskDiagnosisMsg{taskID: taskID, chat: ch, answer: firstAnswer(turns, diagnoseOpeningLines), err: err}
	}
}

func (t *taskView) applyDiagnosis(msg taskDiagnosisMsg) {
	if msg.taskID != t.detail.taskID || msg.chat.ID != t.diagnosis.chatID {
		return
	}
	t.diagnosis.fetching = false
	t.diagnosis.answer, t.diagnosis.err = msg.answer, msg.err
}

// diagnosisLines are the card's diagnosis section: the chat it came from,
// whether it is about this stop or an earlier one at the same step for the
// same reason, and its answer's opening lines. Empty when there is none to
// show.
func (t *taskView) diagnosisLines(c failureCard, width int) []string {
	d := t.diagnosis
	ch, ok := diagnosisChat(t.chats, c)
	if !ok || d.chatID != ch.ID || d.fetching || d.err != nil || len(d.answer) == 0 {
		return nil
	}
	title := "Diagnosis"
	head := fmt.Sprintf("chat #%d · %s", ch.ID, ch.Agent)
	if earlierStop(ch, c) {
		title = "Stopped here before"
		head = fmt.Sprintf("same step, same reason · chat #%d (%s) said:", ch.ID, ch.Agent)
	}
	out := []string{"", section(title), "", styleDim.Render(ansi.Truncate("  "+head, max(width, 1), "…"))}
	for _, line := range d.answer {
		out = append(out, wrapText(line, width, wrapOpts{prefix: "    ", indent: 4, maxLines: 2})...)
	}
	return out
}

// earlierStop is whether ch was asked about an earlier stop than the card's:
// it was opened before the attempt the card names finished.
func earlierStop(ch apiclient.Chat, c failureCard) bool {
	if !c.hasRun || c.run.FinishedAt == nil {
		return false
	}
	return ch.CreatedAt.Before(c.run.FinishedAt.Add(-time.Millisecond))
}
