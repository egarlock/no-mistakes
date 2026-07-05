package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

// writeProfile lays down a profile directory under nmHome/profiles/<name>/ with
// the given profile.yaml body and extra files, returning the profile dir.
func writeProfile(t *testing.T, nmHome, name, profileYAML string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(nmHome, "profiles", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(profileYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newProfileManager(nmHome string) *RunManager {
	return &RunManager{paths: paths.WithRoot(nmHome)}
}

// A missing profile directory / profile.yaml fails loud: loadProfile returns an
// error so the run fails at start rather than silently dropping to the default
// pipeline.
func TestLoadProfile_MissingFailsLoud(t *testing.T) {
	m := newProfileManager(t.TempDir())
	if _, _, err := m.loadProfile("team-ios"); err == nil {
		t.Fatal("expected an error for a missing profile (fail closed)")
	}
}

// An unparsable profile.yaml fails loud too.
func TestLoadProfile_UnparsableFailsLoud(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-ios", "steps: [oops\n", nil)
	m := newProfileManager(nmHome)
	if _, _, err := m.loadProfile("team-ios"); err == nil {
		t.Fatal("expected an error for an unparsable profile.yaml")
	}
}

// An unsafe profile name is rejected before any filesystem read.
func TestLoadProfile_UnsafeNameRejected(t *testing.T) {
	m := newProfileManager(t.TempDir())
	for _, bad := range []string{"../escape", "team/ios", "Team", "", ".hidden"} {
		if _, _, err := m.loadProfile(bad); err == nil {
			t.Errorf("expected an error for unsafe profile name %q", bad)
		}
	}
}

// A profile.yaml that parses to zero steps — empty file, or a typo'd key like
// `step:` — must fail loud: BuildPipeline treats an empty steps list as "run
// the default pipeline", so accepting it would silently replace the team gate
// with the default pipeline (the exact silent fallback the docs promise never
// happens).
func TestLoadProfile_EmptyStepsFailsLoud(t *testing.T) {
	for name, yaml := range map[string]string{
		"empty file":       "",
		"version only":     "version: 1\n",
		"empty steps list": "version: 1\nsteps: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			nmHome := t.TempDir()
			writeProfile(t, nmHome, "team-ios", yaml, nil)
			m := newProfileManager(nmHome)
			_, _, err := m.loadProfile("team-ios")
			if err == nil {
				t.Fatal("expected an error for a profile with no steps (fail closed)")
			}
			if !strings.Contains(err.Error(), "defines no steps") {
				t.Errorf("error = %v, want it to name the zero-steps problem", err)
			}
		})
	}
}

// A typo'd key (e.g. `step:` for `steps:`) must fail parsing, not silently
// yield zero steps: a profile is host-authored config with exactly two legal
// keys, so strict parsing is cheap and matches the fail-loud contract.
func TestLoadProfile_UnknownKeyFailsLoud(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-ios", "version: 1\nstep:\n  - review\n  - push\n", nil)
	m := newProfileManager(nmHome)
	if _, _, err := m.loadProfile("team-ios"); err == nil {
		t.Fatal("expected an error for an unknown key in profile.yaml (typo must fail loud)")
	}
}

// A shared profile must not carry `mode: revise` skill steps: one profile edit
// would then mutate and auto-commit on every repo pointing at the profile, a
// blast-radius posture that is deliberately deferred. Rejection happens at
// profile load (fail the run at start) because after ComposeProfileSteps the
// merged list no longer knows which steps came from the profile.
func TestLoadProfile_RejectsReviseMode(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-ios",
		"steps:\n  - rebase\n  - name: house-style\n    type: skill\n    skill: skills/revise.md\n    mode: revise\n  - push\n",
		map[string]string{"skills/revise.md": "revise body"})
	m := newProfileManager(nmHome)
	_, _, err := m.loadProfile("team-ios")
	if err == nil {
		t.Fatal("expected an error for a revise-mode skill step in a shared profile")
	}
	if !strings.Contains(err.Error(), "mode: revise") || !strings.Contains(err.Error(), "house-style") {
		t.Errorf("error = %v, want it to name the revise step", err)
	}

	// Review-mode skill steps stay allowed.
	writeProfile(t, nmHome, "team-ok",
		"steps:\n  - rebase\n  - name: ios-review\n    type: skill\n    skill: skills/review.md\n    mode: review\n  - push\n",
		map[string]string{"skills/review.md": "review body"})
	if _, _, err := m.loadProfile("team-ok"); err != nil {
		t.Fatalf("review-mode profile should load: %v", err)
	}
}

// A selected profile that cannot be verified against the trusted default
// branch (fetch/resolve failure → empty trustedSHA) must stop the run, not
// silently degrade to the default pipeline. When the trusted read succeeded,
// the trusted copy is authoritative and the pushed value never matters.
// A host-local binding needs no trusted SHA at all: nothing about it comes
// from the repo, so the check applies only to the repo-config path.
func TestUnverifiedProfileError(t *testing.T) {
	if err := unverifiedProfileError("", "", &config.RepoConfig{Profile: "team-ios"}); err == nil {
		t.Fatal("expected an error: profile selected but the default branch could not be fetched")
	} else if !strings.Contains(err.Error(), "team-ios") {
		t.Errorf("error = %v, want it to name the profile", err)
	}
	if err := unverifiedProfileError("", "", &config.RepoConfig{}); err != nil {
		t.Errorf("no profile named: want nil, got %v", err)
	}
	if err := unverifiedProfileError("", "", nil); err != nil {
		t.Errorf("nil pushed config: want nil, got %v", err)
	}
	if err := unverifiedProfileError("", "abc123", &config.RepoConfig{Profile: "team-ios"}); err != nil {
		t.Errorf("trusted SHA resolved: want nil (trusted copy is authoritative), got %v", err)
	}
	// Host-local binding: machine-owner-authored, does not need the default
	// branch to resolve — even when the pushed config also names a profile.
	if err := unverifiedProfileError("team-local", "", &config.RepoConfig{Profile: "team-ios"}); err != nil {
		t.Errorf("local binding with empty trusted SHA: want nil, got %v", err)
	}
	if err := unverifiedProfileError("team-local", "", nil); err != nil {
		t.Errorf("local binding with nil pushed config: want nil, got %v", err)
	}
}

// A host-local repo→profile binding (`no-mistakes profile use`) wins over the
// repo config's trusted `profile:` field: it is authored by the machine owner,
// so it carries the same trust level as the global config.
func TestResolveProfilePipeline_LocalBindingWinsOverRepoConfig(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-local", "steps:\n  - rebase\n  - push\n", nil)
	writeProfile(t, nmHome, "team-repo", "steps:\n  - review\n  - push\n", nil)
	m := newProfileManager(nmHome)

	got, err := m.resolveProfilePipeline(t.Context(), "test-run", "test-branch", "team-local", "team-repo", nil, "")
	if err != nil {
		t.Fatalf("resolveProfilePipeline: %v", err)
	}
	if len(got.steps) != 2 || got.steps[0].Name != "rebase" {
		t.Fatalf("steps = %+v, want the LOCAL binding's steps (rebase, push)", got.steps)
	}
	if !strings.HasPrefix(got.stamp, "team-local@") {
		t.Errorf("stamp = %q, want it to record the locally-bound profile", got.stamp)
	}
}

// With no repo-config profile at all (the work-repo case: no .no-mistakes.yaml
// committed), the local binding alone selects the profile and its steps become
// the whole pipeline.
func TestResolveProfilePipeline_LocalBindingOnly(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-local", "steps:\n  - rebase\n  - review\n  - push\n", nil)
	m := newProfileManager(nmHome)

	got, err := m.resolveProfilePipeline(t.Context(), "test-run", "test-branch", "team-local", "", nil, "")
	if err != nil {
		t.Fatalf("resolveProfilePipeline: %v", err)
	}
	if len(got.steps) != 3 {
		t.Fatalf("steps = %+v, want the profile's 3 steps as the whole pipeline", got.steps)
	}
	if !strings.HasPrefix(got.stamp, "team-local@") {
		t.Errorf("stamp = %q, want a team-local stamp", got.stamp)
	}
}

// A local binding to a missing/broken profile fails the run at start — it must
// never fall back to the repo-config profile or the default pipeline.
func TestResolveProfilePipeline_LocalBindingMissingProfileFails(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-repo", "steps:\n  - review\n  - push\n", nil)
	m := newProfileManager(nmHome)

	_, err := m.resolveProfilePipeline(t.Context(), "test-run", "test-branch", "team-absent", "team-repo", nil, "")
	if err == nil {
		t.Fatal("expected an error for a local binding to a missing profile (fail closed, no fallback)")
	}
	if !strings.Contains(err.Error(), "team-absent") {
		t.Errorf("error = %v, want it to name the locally-bound profile", err)
	}
}

// Repo `steps:` with a `- use: profile` splice sentinel compose with a
// locally-bound profile exactly like the repo-config path.
func TestResolveProfilePipeline_SpliceCompositionWithLocalBinding(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-local", "steps:\n  - review\n  - lint\n", nil)
	m := newProfileManager(nmHome)

	repoSteps := []config.StepSpec{
		{Name: "rebase"},
		{Use: config.ProfileSpliceSentinel},
		{Name: "push"},
	}
	got, err := m.resolveProfilePipeline(t.Context(), "test-run", "test-branch", "team-local", "", repoSteps, "repo instructions")
	if err != nil {
		t.Fatalf("resolveProfilePipeline: %v", err)
	}
	want := []string{"rebase", "review", "lint", "push"}
	if len(got.steps) != len(want) {
		t.Fatalf("steps = %+v, want %v", got.steps, want)
	}
	for i, name := range want {
		if got.steps[i].Name != name {
			t.Errorf("steps[%d] = %q, want %q", i, got.steps[i].Name, name)
		}
	}
	if !strings.Contains(got.instructions, "repo instructions") {
		t.Errorf("instructions = %q, want the repo instructions preserved", got.instructions)
	}
}

// No profile selected anywhere: repo steps and instructions pass through
// untouched and no stamp is produced.
func TestResolveProfilePipeline_NoProfilePassthrough(t *testing.T) {
	m := newProfileManager(t.TempDir())
	repoSteps := []config.StepSpec{{Name: "rebase"}, {Name: "push"}}
	got, err := m.resolveProfilePipeline(t.Context(), "test-run", "test-branch", "", "", repoSteps, "repo instructions")
	if err != nil {
		t.Fatalf("resolveProfilePipeline: %v", err)
	}
	if len(got.steps) != 2 || got.steps[0].Name != "rebase" {
		t.Fatalf("steps = %+v, want the repo steps unchanged", got.steps)
	}
	if got.instructions != "repo instructions" {
		t.Errorf("instructions = %q, want passthrough", got.instructions)
	}
	if got.stamp != "" {
		t.Errorf("stamp = %q, want empty when no profile gates the run", got.stamp)
	}
}

// A stray `- use: profile` sentinel with no profile selected (neither local
// binding nor repo config) still fails loud.
func TestResolveProfilePipeline_StraySpliceFails(t *testing.T) {
	m := newProfileManager(t.TempDir())
	repoSteps := []config.StepSpec{{Use: config.ProfileSpliceSentinel}}
	if _, err := m.resolveProfilePipeline(t.Context(), "test-run", "test-branch", "", "", repoSteps, ""); err == nil {
		t.Fatal("expected an error for a splice sentinel with no profile selected")
	}
}

func TestLoadProfile_ParsesSteps(t *testing.T) {
	nmHome := t.TempDir()
	writeProfile(t, nmHome, "team-ios", "version: 2\nsteps:\n  - rebase\n  - push\n", nil)
	m := newProfileManager(nmHome)
	profile, dir, err := m.loadProfile("team-ios")
	if err != nil {
		t.Fatalf("loadProfile: %v", err)
	}
	if profile.Version != 2 {
		t.Errorf("version = %d, want 2", profile.Version)
	}
	if len(profile.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(profile.Steps))
	}
	if dir != m.paths.ProfileDir("team-ios") {
		t.Errorf("dir = %q, want %q", dir, m.paths.ProfileDir("team-ios"))
	}
}

// A profile's skill body is read from the profile directory on disk, never from
// a repo worktree. The body content proves the disk read happened.
func TestLoadProfileSkillBodies_ReadsFromProfileDir(t *testing.T) {
	nmHome := t.TempDir()
	const body = "---\nname: ios-review\nmode: review\n---\nFlag force unwraps."
	dir := writeProfile(t, nmHome, "team-ios",
		"steps:\n  - name: ios-review\n    type: skill\n    skill: skills/ios-review.md\n    mode: review\n",
		map[string]string{"skills/ios-review.md": body})

	specs := []config.StepSpec{
		{Name: "rebase"},
		{Name: "ios-review", Skill: "skills/ios-review.md", Mode: "review"},
	}
	got := loadProfileSkillBodies(dir, specs, "test-run")
	if got[0].SkillBody != "" {
		t.Errorf("non-skill spec should get no body, got %q", got[0].SkillBody)
	}
	if !strings.Contains(got[1].SkillBody, "Flag force unwraps") {
		t.Errorf("skill body = %q, want the profile-dir file content", got[1].SkillBody)
	}
}

// A skill path that escapes the profile directory is refused: the body stays
// empty (the step will then park with a misconfiguration finding) and no file
// outside the profile dir is read.
func TestLoadProfileSkillBodies_PathEscapeRefused(t *testing.T) {
	nmHome := t.TempDir()
	// A secret file a step must not be able to read via path traversal.
	secret := filepath.Join(nmHome, "secret.md")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := writeProfile(t, nmHome, "team-ios", "steps: []\n", nil)

	specs := []config.StepSpec{
		{Name: "sneaky", Skill: "../secret.md", Mode: "review"},
	}
	got := loadProfileSkillBodies(dir, specs, "test-run")
	if got[0].SkillBody != "" {
		t.Fatalf("SECURITY: escaping skill path was read: %q", got[0].SkillBody)
	}
}

// A missing profile skill file yields an empty body (the step parks), not an error.
func TestLoadProfileSkillBodies_MissingFileEmptyBody(t *testing.T) {
	nmHome := t.TempDir()
	dir := writeProfile(t, nmHome, "team-ios", "steps: []\n", nil)
	specs := []config.StepSpec{{Name: "ios-review", Skill: "skills/absent.md", Mode: "review"}}
	got := loadProfileSkillBodies(dir, specs, "test-run")
	if got[0].SkillBody != "" {
		t.Errorf("want empty body for a missing skill file, got %q", got[0].SkillBody)
	}
}

func TestLoadProfileStepInstructions_ReadsFromProfileDir(t *testing.T) {
	nmHome := t.TempDir()
	dir := writeProfile(t, nmHome, "team-ios", "steps: []\n",
		map[string]string{"instructions/swift.md": "Prefer guard-let."})
	specs := []config.StepSpec{
		{Name: "review", Instructions: []string{"instructions/swift.md"}},
	}
	got := loadProfileStepInstructions(dir, specs, "test-run")
	if !strings.Contains(got, "Prefer guard-let") {
		t.Errorf("instructions = %q, want the profile-dir file content", got)
	}
}

func TestLoadProfileStepInstructions_PathEscapeSkipped(t *testing.T) {
	nmHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(nmHome, "secret.md"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := writeProfile(t, nmHome, "team-ios", "steps: []\n", nil)
	specs := []config.StepSpec{{Name: "review", Instructions: []string{"../secret.md"}}}
	if got := loadProfileStepInstructions(dir, specs, "test-run"); got != "" {
		t.Fatalf("SECURITY: escaping instruction path was read: %q", got)
	}
}

func TestProfilePathWithinDir(t *testing.T) {
	dir := "/home/u/.no-mistakes/profiles/team-ios"
	cases := []struct {
		rel  string
		want bool
	}{
		{"skills/review.md", true},
		{"a/b/c.md", true},
		{"../secret.md", false},
		{"../../etc/passwd", false},
		{"/etc/passwd", false},
		{"", false},
	}
	for _, c := range cases {
		if _, ok := ProfilePathWithinDir(dir, c.rel); ok != c.want {
			t.Errorf("ProfilePathWithinDir(%q) safe=%v, want %v", c.rel, ok, c.want)
		}
	}
}

func TestJoinInstructionSections(t *testing.T) {
	if got := joinInstructionSections("", ""); got != "" {
		t.Errorf("empty inputs = %q, want empty", got)
	}
	if got := joinInstructionSections("A", ""); got != "A" {
		t.Errorf("= %q, want A", got)
	}
	if got := joinInstructionSections("A", "B"); got != "A\n\nB" {
		t.Errorf("= %q, want A\\n\\nB", got)
	}
}
