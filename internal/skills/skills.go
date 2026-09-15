// Package skills implements the Agent Skills open standard.
// See https://agentskills.io for the specification.
package skills

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/charlievieth/fastwalk"
	"github.com/charmbracelet/crush/internal/pubsub"
	"gopkg.in/yaml.v3"
)

const (
	SkillFileName          = "SKILL.md"
	MaxNameLength          = 64
	MaxDescriptionLength   = 1024
	MaxCompatibilityLength = 500
)

var (
	namePattern    = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)
	promptReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")

	latestStates   []*SkillState
	latestStatesMu sync.RWMutex
)

// Skill represents a parsed SKILL.md file.
type Skill struct {
	Name                   string            `yaml:"name" json:"name"`
	Description            string            `yaml:"description" json:"description"`
	UserInvocable          bool              `yaml:"user-invocable" json:"user_invocable"`
	DisableModelInvocation bool              `yaml:"disable-model-invocation" json:"disable_model_invocation"`
	License                string            `yaml:"license,omitempty" json:"license,omitempty"`
	Compatibility          string            `yaml:"compatibility,omitempty" json:"compatibility,omitempty"`
	Metadata               map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Instructions           string            `yaml:"-" json:"instructions"`
	Path                   string            `yaml:"-" json:"path"`
	SkillFilePath          string            `yaml:"-" json:"skill_file_path"`
	Builtin                bool              `yaml:"-" json:"builtin"`
}

// DiscoveryState represents the outcome of discovering a single skill file.
type DiscoveryState int

const (
	// StateNormal indicates the skill was parsed and validated successfully.
	StateNormal DiscoveryState = iota
	// StateError indicates discovery encountered a scan/parse/validate error.
	StateError
)

// SkillState represents the latest discovery status of a skill file.
// Invocation flags are copied from the parsed Skill so UI clients can
// report which trigger paths (user command, model invocation) are
// available without re-reading the source file.
type SkillState struct {
	Name                   string
	Path                   string
	State                  DiscoveryState
	Err                    error
	UserInvocable          bool
	DisableModelInvocation bool
}

// state snapshots the skill's discovery state for the given path.
func (s *Skill) state(path string, state DiscoveryState, err error) *SkillState {
	return &SkillState{
		Name:                   s.Name,
		Path:                   path,
		State:                  state,
		Err:                    err,
		UserInvocable:          s.UserInvocable,
		DisableModelInvocation: s.DisableModelInvocation,
	}
}

// Event is published when skill discovery completes.
type Event struct {
	States []*SkillState
}

var broker = pubsub.NewBroker[Event]()

// SubscribeEvents returns a channel that receives events when skill discovery state changes.
func SubscribeEvents(ctx context.Context) <-chan pubsub.Event[Event] {
	return broker.Subscribe(ctx)
}

// PublishStates publishes a skill discovery event with the given states.
func PublishStates(states []*SkillState) {
	broker.Publish(pubsub.UpdatedEvent, Event{States: cloneStates(states)})
}

// cloneStates returns a deep copy of the given state slice so callers cannot
// accidentally mutate the source.
func cloneStates(states []*SkillState) []*SkillState {
	if states == nil {
		return nil
	}
	result := make([]*SkillState, len(states))
	for i, s := range states {
		clone := *s
		result[i] = &clone
	}
	return result
}

// GetLatestStates returns the latest discovery states.
func GetLatestStates() []*SkillState {
	latestStatesMu.RLock()
	defer latestStatesMu.RUnlock()
	return cloneStates(latestStates)
}

// SetLatestStates stores the given states in the package-level cache so that
// GetLatestStates can return them synchronously before the first pubsub event
// arrives.
func SetLatestStates(states []*SkillState) {
	latestStatesMu.Lock()
	latestStates = cloneStates(states)
	latestStatesMu.Unlock()
}

// Validate checks if the skill meets spec requirements.
func (s *Skill) Validate() error {
	var errs []error

	if s.Name == "" {
		errs = append(errs, errors.New("name is required"))
	} else {
		if len(s.Name) > MaxNameLength {
			errs = append(errs, fmt.Errorf("name exceeds %d characters", MaxNameLength))
		}
		if !namePattern.MatchString(s.Name) {
			errs = append(errs, errors.New("name must be alphanumeric with hyphens, no leading/trailing/consecutive hyphens"))
		}
		if s.Path != "" && !strings.EqualFold(filepath.Base(s.Path), s.Name) {
			errs = append(errs, fmt.Errorf("name %q must match directory %q", s.Name, filepath.Base(s.Path)))
		}
	}

	if s.Description == "" {
		errs = append(errs, errors.New("description is required"))
	} else if len(s.Description) > MaxDescriptionLength {
		errs = append(errs, fmt.Errorf("description exceeds %d characters", MaxDescriptionLength))
	}

	if len(s.Compatibility) > MaxCompatibilityLength {
		errs = append(errs, fmt.Errorf("compatibility exceeds %d characters", MaxCompatibilityLength))
	}

	return errors.Join(errs...)
}

// Parse parses a SKILL.md file from disk.
func Parse(path string) (*Skill, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	skill, err := ParseContent(content)
	if err != nil {
		return nil, err
	}

	skill.Path = filepath.Dir(path)
	skill.SkillFilePath = path

	return skill, nil
}

// ParseContent parses a SKILL.md from raw bytes.
func ParseContent(content []byte) (*Skill, error) {
	frontmatter, body, err := splitFrontmatter(string(content))
	if err != nil {
		return nil, err
	}

	var skill Skill
	if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	skill.Instructions = strings.TrimSpace(body)

	return &skill, nil
}

// splitFrontmatter extracts YAML frontmatter and body from markdown content.
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	// Strip UTF-8 BOM for compatibility with editors that include it.
	content = strings.TrimPrefix(content, "\uFEFF")
	// Normalize line endings to \n for consistent parsing.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	lines := strings.Split(content, "\n")
	start := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) != ""
	})
	if start == -1 || strings.TrimSpace(lines[start]) != "---" {
		return "", "", errors.New("no YAML frontmatter found")
	}

	endOffset := slices.IndexFunc(lines[start+1:], func(line string) bool {
		return strings.TrimSpace(line) == "---"
	})
	if endOffset == -1 {
		return "", "", errors.New("unclosed frontmatter")
	}
	end := start + 1 + endOffset

	frontmatter = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return frontmatter, body, nil
}

// Discover finds all valid skills in the given paths.
func Discover(paths []string) []*Skill {
	skills, _ := DiscoverWithStates(paths)
	return skills
}

// DiscoverWithStates finds all valid skills in the given paths and also
// returns a per-file state slice describing parse/validation outcomes. Useful
// for diagnostics and UI reporting.
func DiscoverWithStates(paths []string) ([]*Skill, []*SkillState) {
	var skills []*Skill
	var states []*SkillState
	var mu sync.Mutex
	seen := make(map[string]bool)
	addState := func(state *SkillState) {
		mu.Lock()
		states = append(states, state)
		mu.Unlock()
	}

	for _, base := range paths {
		// We use fastwalk with Follow: true instead of filepath.WalkDir because
		// WalkDir doesn't follow symlinked directories at any depth—only entry
		// points. This ensures skills in symlinked subdirectories are discovered.
		// fastwalk is concurrent, so we protect shared state (seen, skills) with mu.
		conf := fastwalk.Config{
			Follow:  true,
			ToSlash: fastwalk.DefaultToSlash(),
		}
		err := fastwalk.Walk(&conf, base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				slog.Warn("Failed to walk skills path entry", "base", base, "path", path, "error", err)
				addState(&SkillState{Path: path, State: StateError, Err: err})
				return nil
			}
			if d.IsDir() || d.Name() != SkillFileName {
				return nil
			}
			mu.Lock()
			if seen[path] {
				mu.Unlock()
				return nil
			}
			seen[path] = true
			mu.Unlock()
			skill, err := Parse(path)
			if err != nil {
				slog.Warn("Failed to parse skill file", "path", path, "error", err)
				addState(&SkillState{Path: path, State: StateError, Err: err})
				return nil
			}
			if err := skill.Validate(); err != nil {
				slog.Warn("Skill validation failed", "path", path, "error", err)
				addState(skill.state(path, StateError, err))
				return nil
			}
			slog.Debug("Successfully loaded skill", "name", skill.Name, "path", path)
			mu.Lock()
			skills = append(skills, skill)
			mu.Unlock()
			addState(skill.state(path, StateNormal, nil))
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			slog.Warn("Failed to walk skills path", "path", base, "error", err)
		}
	}

	// fastwalk traversal order is non-deterministic, so sort for stable output.
	// Sort by path first, then alphabetically by name within each path.
	slices.SortStableFunc(skills, func(a, b *Skill) int {
		if c := strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path)); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	return skills, states
}

// ToPromptXML generates XML for injection into the system prompt.
// Skills with DisableModelInvocation set to true are excluded.
func ToPromptXML(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	for _, s := range skills {
		// Skip skills that have disable-model-invocation set
		if s.DisableModelInvocation {
			continue
		}
		sb.WriteString("  <skill>\n")
		fmt.Fprintf(&sb, "    <name>%s</name>\n", escape(s.Name))
		fmt.Fprintf(&sb, "    <description>%s</description>\n", escape(s.Description))
		fmt.Fprintf(&sb, "    <location>%s</location>\n", escape(s.SkillFilePath))
		if s.Builtin {
			sb.WriteString("    <type>builtin</type>\n")
		}
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// skillUsageBlock tells the model how to consume the available-skills
// catalog. It is appended alongside ToPromptXML by PromptBlock so a
// skills-enabled prompt always carries the usage guidance.
const skillUsageBlock = "<skills_usage>\n" +
	"Each `<description>` is a trigger for when a skill applies - it is not the specification. " +
	"Before any other tool call for a matching task, `view` its `<location>` and follow the SKILL.md body; " +
	"do not infer a skill's behavior from its name or description. " +
	"Builtin skills use virtual `crush://skills/...` locations, passed verbatim to view.\n" +
	"</skills_usage>"

// PromptBlock renders the model-facing skill catalog plus its usage
// guidance for the given active set. It returns "" when there is no
// model-visible skill, so callers can append it to a system prompt
// unconditionally.
//
// Unlike the system prompt, which is built once per agent, this is
// evaluated per run so a session's own disabled-skills set is reflected
// in what the model sees.
func PromptBlock(active []*Skill) string {
	if len(active) == 0 {
		return ""
	}
	return ToPromptXML(active) + "\n\n" + skillUsageBlock
}

// FormatInvocation generates XML for a skill when invoked as a user command.
func (s *Skill) FormatInvocation() string {
	var sb strings.Builder
	sb.WriteString("<loaded_skill>\n")
	fmt.Fprintf(&sb, "  <name>%s</name>\n", escape(s.Name))
	fmt.Fprintf(&sb, "  <description>%s</description>\n", escape(s.Description))
	fmt.Fprintf(&sb, "  <location>%s</location>\n", escape(s.SkillFilePath))
	sb.WriteString("  <instructions>\n")
	sb.WriteString(escape(s.Instructions))
	sb.WriteString("\n  </instructions>\n")
	sb.WriteString("</loaded_skill>")
	return sb.String()
}

func escape(s string) string {
	return promptReplacer.Replace(s)
}

// DeduplicateStates removes duplicate skill states by name. When duplicates exist,
// the last occurrence wins (consistent with Deduplicate for skills).
func DeduplicateStates(all []*SkillState) []*SkillState {
	seen := make(map[string]int, len(all))
	for i, s := range all {
		if s.Name != "" {
			seen[s.Name] = i
		}
	}

	result := make([]*SkillState, 0, len(seen))
	for i, s := range all {
		// If it's the last occurrence of this name, or it has no name (error state), keep it
		if s.Name == "" || seen[s.Name] == i {
			result = append(result, s)
		}
	}
	return result
}

// Deduplicate removes duplicate skills by name. When duplicates exist, the
// last occurrence wins. This means user skills (appended after builtins)
// override builtin skills with the same name.
func Deduplicate(all []*Skill) []*Skill {
	seen := make(map[string]int, len(all))
	for i, s := range all {
		seen[s.Name] = i
	}

	result := make([]*Skill, 0, len(seen))
	for i, s := range all {
		if seen[s.Name] == i {
			result = append(result, s)
		}
	}
	return result
}

// ApproxTokenCount returns a rough estimate of how many tokens a string
// occupies when sent to an LLM. Uses the common ~4-chars-per-token heuristic
// that approximates GPT/Claude tokenizers well enough for diagnostic logging.
func ApproxTokenCount(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// Filter removes skills whose names appear in the disabled list.
func Filter(all []*Skill, disabled []string) []*Skill {
	if len(disabled) == 0 {
		return all
	}

	disabledSet := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = true
	}

	result := make([]*Skill, 0, len(all))
	for _, s := range all {
		if !disabledSet[s.Name] {
			result = append(result, s)
		}
	}
	return result
}
