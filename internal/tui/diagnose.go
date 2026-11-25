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

// diagnosisChats are the closed diagnosis chats on the task for the card's
// step and reason, newest first. A chat still open is not one yet: its
// answer may still be coming, and it holds the task.
//
// They are candidates, not answers (#1153): a chat whose first turn was
// refused, failed or was cancelled is closed with nothing to show, and the
// newest of them must not hide an older chat that did answer.
// findDiagnosis walks them.
func diagnosisChats(chats []apiclient.Chat, c failureCard) []apiclient.Chat {
	if c.reason == "" {
		return nil
	}
	var out []apiclient.Chat
	for _, ch := range chats {
		if ch.State != string(chatstate.Closed) {
			continue
		}
		step, reason, ok := parseDiagnoseTitle(ch.Title)
		if !ok || step != c.cardStepNum() || reason != c.reason {
			continue
		}
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// diagnoseCandidateReads bounds findDiagnosis: past a few answerless chats
// in a row an older answer is stale anyway, and every read is a request.
const diagnoseCandidateReads = 5

// findDiagnosis reads candidates newest first and returns the first with an
// answer, and its answer's opening (#1153). err is a read that failed before
// any answer was found; the walk stops there rather than skip past a chat
// it could not read, which may be the one that answered.
func findDiagnosis(cands []apiclient.Chat, turns func(id int64) ([]apiclient.ChatTurn, error),
) (ch apiclient.Chat, answer []string, err error) {
	for i, cand := range cands {
		if i == diagnoseCandidateReads {
			break
		}
		ts, err := turns(cand.ID)
		if err != nil {
			return apiclient.Chat{}, nil, err
		}
		if a := firstAnswer(ts, diagnoseOpeningLines); len(a) > 0 {
			return cand, a, nil
		}
	}
	return apiclient.Chat{}, nil, nil
}

// chatTurnsOf is findDiagnosis's read through the client.
func chatTurnsOf(ctx context.Context, client *apiclient.Client) func(int64) ([]apiclient.ChatTurn, error) {
	return func(id int64) ([]apiclient.ChatTurn, error) {
		_, turns, err := client.GetChat(ctx, id)
		return turns, err
	}
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

// diagnoseCmd is `Q`. A chat already open on the task is reopened, as `T`
// does: nothing new starts. Otherwise it asks first, on the action bar
// (#1162): a yes starts an agent turn, which spends money and runs with the
// task's permissions, and a stray `Q` must not do that unasked. handled is
// whether the key meant diagnose here at all.
func diagnoseCmd(t taskActions, bar *actionBar) (cmd tea.Cmd, handled bool) {
	if !t.offersDiagnose() {
		return nil, false
	}
	if t.openChatID != 0 {
		id, pid := t.openChatID, t.projectID
		return func() tea.Msg { return openChatMsg{id: id, projectID: pid} }, true
	}
	bar.resetPending()
	bar.pending, bar.pendingID = actionDiagnose, t.id
	bar.status().clear()
	return nil, true
}

// diagnoseRun is the yes to `Q`'s question: it reads what the failure card
// would show — the task, the failing attempt's transcript and any earlier
// diagnosis of the same stop — and opens a chat on the task with the
// question already sent. It fetches for itself, so the board's key and the
// workspace's are one path.
func diagnoseRun(client *apiclient.Client, id int64, bar *actionBar) tea.Cmd {
	if client == nil {
		bar.setStatus("not connected", true)
		return nil
	}
	bar.setStatus("asking an agent why it stopped…", false)
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
			if ch, a, err := findDiagnosis(diagnosisChats(chats, c), chatTurnsOf(ctx, client)); err == nil {
				earlier, answer = ch, a
			}
		}
		message := diagnoseMessage(task, c, ev, earlier, answer)
		chat, err := client.OpenTaskChat(context.Background(), id, apiclient.OpenTaskChatRequest{
			Title:   diagnoseTitle(c.cardStepNum(), c.cardStepName(), c.reason),
			Message: message,
		})
		if err != nil {
			if chatID, locked := apiclient.TaskLockedByChat(err); locked {
				return taskChatOpenedMsg{taskID: id, chatID: chatID}
			}
			return taskChatOpenedMsg{taskID: id, err: err}
		}
		if chat.FirstTurn == nil {
			return diagnoseRefused(client, id, chat, message)
		}
		return taskChatOpenedMsg{taskID: id, chatID: chat.ID}
	}
}

// diagnoseRefused settles a diagnosis whose chat the daemon created but
// whose question it did not send — the chat cap reached, say (#1153). The
// create is not undone by the daemon (task 161 decision 1), and an empty
// chat titled as a diagnosis would hold the task locked while asking
// nothing. So `Q` closes it, through the same close the chat workspace
// offers, and the bar says why the question was not asked. A close that
// fails falls back to what `chat with…` does with a refused send: the chat
// opens with the question back in its composer and the reason as its note,
// so the human can send it or close it.
func diagnoseRefused(client *apiclient.Client, taskID int64, chat *apiclient.Chat, message string) taskChatOpenedMsg {
	why := "the first message was not sent"
	if len(chat.Warnings) > 0 {
		why = strings.Join(chat.Warnings, "; ")
	}
	// The bar is one line: what happened leads, and the daemon's reason —
	// already said to be about the first message — follows.
	reason := strings.TrimPrefix(why, "the first message was not sent: ")
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if _, err := client.CloseChat(ctx, chat.ID); err != nil {
		return taskChatOpenedMsg{
			taskID: taskID, chatID: chat.ID, draft: message,
			note: why + " — closing the chat failed (" + actionErrString(err) + "); send the question again or close it",
		}
	}
	return taskChatOpenedMsg{taskID: taskID, err: fmt.Errorf(
		"not asked, chat #%d closed so task #%d stays unlocked — %s", chat.ID, taskID, reason)}
}

// taskDiagnosis is the card's one read of a diagnosis: keyed by the newest
// candidate chat (chatID), and holding the chat the answer was found in —
// that one, or an older one when the newer ones have none (#1153).
type taskDiagnosis struct {
	chatID   int64
	fetching bool
	chat     apiclient.Chat
	answer   []string
	err      error
}

type taskDiagnosisMsg struct {
	taskID int64
	key    int64 // the newest candidate the read was keyed by
	chat   apiclient.Chat
	answer []string
	err    error
}

// syncDiagnosis fetches the diagnosis the card would show, once per newest
// candidate: when the chat list's newest diagnosis chat for the card's step
// and reason is not the one already read from. It runs after every message,
// as syncFailureEvidence does.
func (t *taskView) syncDiagnosis() tea.Cmd {
	if !t.showsFailureCard() {
		return nil
	}
	cands := diagnosisChats(t.chats, t.failureCard())
	if len(cands) == 0 || t.diagnosis.chatID == cands[0].ID {
		return nil
	}
	client, taskID, key := t.detail.client, t.detail.taskID, cands[0].ID
	t.diagnosis = taskDiagnosis{chatID: key, fetching: client != nil}
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		ch, answer, err := findDiagnosis(cands, chatTurnsOf(ctx, client))
		return taskDiagnosisMsg{taskID: taskID, key: key, chat: ch, answer: answer, err: err}
	}
}

func (t *taskView) applyDiagnosis(msg taskDiagnosisMsg) {
	if msg.taskID != t.detail.taskID || msg.key != t.diagnosis.chatID {
		return
	}
	t.diagnosis.fetching = false
	t.diagnosis.chat, t.diagnosis.answer, t.diagnosis.err = msg.chat, msg.answer, msg.err
}

// diagnosisLines are the card's diagnosis section: the chat it came from,
// whether it is about this stop or an earlier one at the same step for the
// same reason, and its answer's opening lines. Empty when there is none to
// show.
func (t *taskView) diagnosisLines(c failureCard, width int) []string {
	d, ch := t.diagnosis, t.diagnosis.chat
	cands := diagnosisChats(t.chats, c)
	if len(cands) == 0 || d.chatID != cands[0].ID || d.fetching || d.err != nil || len(d.answer) == 0 {
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
