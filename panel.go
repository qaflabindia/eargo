package ear

import (
	"context"
	"fmt"
	"strings"
)

// Panel is multi-persona deliberation, native to the runtime.
//
// A workflow authored with a `Pattern:` line in workflow.md convenes its
// personas as a panel instead of reasoning single-voiced -- and the pattern
// is prose, not an enum: "Pattern: adversarial debate, the risk officer has
// the last word" goes into the prompt verbatim, so the deliberation *style*
// is itself a natural-language instruction the model follows, never a
// hardcoded protocol.
//
// Who speaks next is a judgment, not a rotation: each turn, a moderator
// judgment reads the pattern and the transcript and chooses the next speaker
// -- or concludes the panel early when it has genuinely converged. Code
// guards what the model may not decide: only listed personas speak (an
// unreadable choice falls back to rotation, on the record), conclusion is
// honored only once every persona has spoken at least once, and the turn
// budget caps the whole conversation regardless. Personas may use the bound
// tools inside their turns -- get the facts, then speak -- with every
// invocation a governed `tool` record exactly as in deliberation.
//
// Everything around the conversation stays the runtime's: the Governor gated
// the cycle before the panel sat, validation checks the synthesis, Contracts
// still judge the deliverable, and every turn lands on the trail (stage
// "conversation") with the synthesis as the cycle's "deliberation" record.
// With no model bound the panel does not fake a debate: it rotates
// deterministically, reports who would have deliberated, and says so.
type Panel struct {
	Rounds   int // how many times the table goes around; default 2
	MaxTurns int // hard cap on total turns regardless; default 12
}

// turnToolBudget is how many tool calls one panel turn may make before the
// persona must speak with what it has -- execution mechanics, not judgment.
const turnToolBudget = 3

// concludeWords are the moderator answers read as "the panel is done".
var concludeWords = map[string]bool{
	"conclude": true, "concluded": true, "consensus": true,
	"done": true, "end": true, "finish": true, "finished": true,
}

// Turn is one panel turn: who spoke, and what they said.
type Turn struct {
	Speaker   string
	Statement string
}

// Convene runs the deliberation: personas speak in judged (or rotated) turns
// over the intent, and the transcript synthesizes into the one decision the
// pipeline continues with. An LM error inside a turn fails the cycle -- the
// panel is the deliberation, not an optional enrichment.
func (p *Panel) Convene(ctx context.Context, rt *Runtime, personas []*Persona, intent Intent, style string) (string, error) {
	rounds := p.Rounds
	if rounds <= 0 {
		rounds = 2
	}
	maxTurns := p.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 12
	}
	budget := rounds * len(personas)
	if budget > maxTurns {
		budget = maxTurns
	}
	live := rt.LM != nil
	var tools []*BoundTool
	if live && rt.ToolBinder != nil {
		tools = rt.ToolBinder.Tools()
	}

	var transcript []Turn
	spoken := map[string]bool{}
	lastIndex := -1
	conclusion := ""

	for len(transcript) < budget {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		persona := personas[(lastIndex+1)%len(personas)]
		chosenBy, choiceRationale := "rotation", ""
		if live {
			choice, rationale, err := p.chooseSpeaker(ctx, rt.LM, intent, personas, transcript, style)
			if err != nil {
				return "", err
			}
			key := Normalize(choice)
			if concludeWords[key] {
				if everyoneSpoke(personas, spoken) {
					conclusion = rationale
					break
				}
				// A panel cannot converge before everyone has spoken --
				// rotation continues, and the record says why.
				chosenBy = "rotation (conclusion refused: not every persona has spoken yet)"
			} else if named := personaNamed(personas, key); named != nil {
				persona, chosenBy, choiceRationale = named, "model", rationale
			} else {
				chosenBy = fmt.Sprintf("rotation (the choice '%s' names no listed persona)", choice)
			}
		}

		var statement string
		var err error
		switch {
		case live && len(tools) > 0:
			statement, err = p.speakWithTools(ctx, rt, intent, persona, transcript, style, tools)
		case live:
			statement, err = p.speak(ctx, rt.LM, intent, persona, transcript, style)
		default:
			instructions := persona.Instructions
			if instructions == "" {
				instructions = "no standing instructions"
			}
			statement = fmt.Sprintf("(no model bound) %s would deliberate here as: %s", persona.Name, instructions)
		}
		if err != nil {
			return "", err
		}

		lastIndex = personaIndex(personas, persona)
		spoken[persona.Name] = true
		transcript = append(transcript, Turn{Speaker: persona.Name, Statement: statement})
		rt.ReasoningLog.Record(Record{
			Stage: "conversation",
			Inputs: map[string]any{
				"speaker": persona.Name, "turn": len(transcript), "style": style,
				"intent": intent.Text, "chosen_by": chosenBy, "choice_rationale": choiceRationale,
			},
			Output: statement,
			Model:  panelModel(live),
		})
	}

	var decision string
	if live {
		out, err := SynthesizePanel.Run(ctx, rt.LM, SynthesizeIn{
			IntentText: intent.Text,
			Pattern:    orOpenDeliberation(style),
			Transcript: renderTranscript(transcript),
		})
		if err != nil {
			return "", err
		}
		decision = strings.TrimSpace(out.Decision)
	} else {
		names := make([]string, len(personas))
		for i, persona := range personas {
			names[i] = persona.Name
		}
		decision = fmt.Sprintf("Panel of %s deliberated '%s'", strings.Join(names, ", "), intent.Text)
		if style != "" {
			decision += fmt.Sprintf(" in the style '%s'", style)
		}
		decision += " -- no model bound, so no judgment was synthesized."
	}

	inputs := map[string]any{
		"intent": intent.Text, "context": intent.Context,
		"panel": personaNames(personas), "style": style,
		"transcript": renderTranscript(transcript),
	}
	if conclusion != "" {
		inputs["concluded_early"] = conclusion
	}
	rt.ReasoningLog.Record(Record{
		Stage:  "deliberation",
		Inputs: inputs,
		Output: decision,
		Model:  panelModel(live),
	})
	return decision, nil
}

// chooseSpeaker asks the moderator judgment who speaks next.
func (p *Panel) chooseSpeaker(ctx context.Context, lm LM, intent Intent, personas []*Persona, transcript []Turn, style string) (string, string, error) {
	table := make([]string, len(personas))
	for i, persona := range personas {
		instructions := persona.Instructions
		if instructions == "" {
			instructions = "no standing instructions"
		}
		table[i] = persona.Name + ": " + instructions
	}
	out, err := ChooseNextSpeaker.Run(ctx, lm, ChooseSpeakerIn{
		IntentText: intent.Text,
		Pattern:    orOpenDeliberation(style),
		Personas:   strings.Join(table, "\n"),
		Transcript: renderTranscript(transcript),
	})
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(out.Speaker), strings.TrimSpace(out.Rationale), nil
}

// speak takes one plain turn as the persona.
func (p *Panel) speak(ctx context.Context, lm LM, intent Intent, persona *Persona, transcript []Turn, style string) (string, error) {
	out, err := SpeakInPanel.Run(ctx, lm, PanelSpeakIn{
		IntentText: intent.Text,
		Persona:    renderPanelPersona(persona),
		Pattern:    orOpenDeliberation(style),
		Transcript: renderTranscript(transcript),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Statement), nil
}

// speakWithTools takes one turn with the native tool loop inside it: the
// persona may call the bound tools for facts -- each invocation a governed
// `tool` record through InvokeTool, exactly as in deliberation -- then
// speaks. Budget spent or no statement given: the persona speaks plainly
// with the gathered facts in view.
func (p *Panel) speakWithTools(ctx context.Context, rt *Runtime, intent Intent, persona *Persona, transcript []Turn, style string, tools []*BoundTool) (string, error) {
	catalogue := toolCatalogue(tools)
	var gathered []string
	for i := 0; i < turnToolBudget; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		gatheredText := "none yet"
		if len(gathered) > 0 {
			gatheredText = strings.Join(gathered, "\n")
		}
		out, err := SpeakOrUseTool.Run(ctx, rt.LM, SpeakOrToolIn{
			IntentText: intent.Text,
			Persona:    renderPanelPersona(persona),
			Pattern:    orOpenDeliberation(style),
			Transcript: renderTranscript(transcript),
			Tools:      catalogue,
			Gathered:   gatheredText,
		})
		if err != nil {
			return "", err
		}
		chosen, ok := rt.ToolBinder.Get(strings.TrimSpace(out.Tool))
		if !ok {
			if statement := strings.TrimSpace(out.Statement); statement != "" {
				return statement, nil
			}
			break // neither a tool nor a statement -- speak plainly below
		}
		args := coerceArgs(out.Arguments)
		result := rt.InvokeTool(ctx, chosen.Name, args) // governed + recorded
		gathered = append(gathered, fmt.Sprintf("%s(%s) -> %s", chosen.Name, renderArgs(args), result))
	}
	rendered := renderTranscript(transcript)
	if len(gathered) > 0 {
		rendered += fmt.Sprintf("\n\n[facts %s gathered with tools]\n%s", persona.Name, strings.Join(gathered, "\n"))
	}
	out, err := SpeakInPanel.Run(ctx, rt.LM, PanelSpeakIn{
		IntentText: intent.Text,
		Persona:    renderPanelPersona(persona),
		Pattern:    orOpenDeliberation(style),
		Transcript: rendered,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Statement), nil
}

// panelCall reads the scheduled plan's authored deliberation pattern, if
// any, and the personas it convenes -- the workflows' styles joined, their
// delegated personas de-duplicated in order. A pattern with fewer than two
// personas has nobody to deliberate with, so single-voiced reasoning
// proceeds as usual.
func panelCall(plan []*Workflow) (string, []*Persona) {
	var styles []string
	var personas []*Persona
	seen := map[*Persona]bool{}
	for _, workflow := range plan {
		if workflow.Pattern == "" {
			continue
		}
		styles = append(styles, workflow.Pattern)
		for _, persona := range workflow.DelegatedPersonas() {
			if !seen[persona] {
				seen[persona] = true
				personas = append(personas, persona)
			}
		}
	}
	return strings.Join(styles, "; "), personas
}

// -- small helpers ----------------------------------------------------------

func renderPanelPersona(persona *Persona) string {
	instructions := persona.Instructions
	if instructions == "" {
		instructions = "no standing instructions"
	}
	lines := []string{persona.Name + ": " + instructions}
	for _, skill := range persona.Skills {
		lines = append(lines, "  - Skill "+skill.Name+": "+skill.Instruction())
	}
	return strings.Join(lines, "\n")
}

func renderTranscript(transcript []Turn) string {
	if len(transcript) == 0 {
		return "no turns yet"
	}
	rendered := make([]string, len(transcript))
	for i, turn := range transcript {
		rendered[i] = "[" + turn.Speaker + "]\n" + turn.Statement
	}
	return strings.Join(rendered, "\n\n")
}

func personaNamed(personas []*Persona, key string) *Persona {
	for _, persona := range personas {
		if Normalize(persona.Name) == key {
			return persona
		}
	}
	return nil
}

func personaIndex(personas []*Persona, persona *Persona) int {
	for i, p := range personas {
		if p == persona {
			return i
		}
	}
	return -1
}

func personaNames(personas []*Persona) []string {
	names := make([]string, len(personas))
	for i, persona := range personas {
		names[i] = persona.Name
	}
	return names
}

func everyoneSpoke(personas []*Persona, spoken map[string]bool) bool {
	for _, persona := range personas {
		if !spoken[persona.Name] {
			return false
		}
	}
	return true
}

func orOpenDeliberation(style string) string {
	if style == "" {
		return "an open deliberation"
	}
	return style
}

// panelModel labels a panel record: "llm" when a model spoke, otherwise
// empty so Record marks it the deterministic fallback.
func panelModel(live bool) string {
	if live {
		return "llm"
	}
	return ""
}
