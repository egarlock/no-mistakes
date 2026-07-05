package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

// setupProfileTestRepo creates a git repo (no daemon), points NM_HOME at an
// isolated temp dir, registers the repo in the database, and chdirs into it so
// findRepo resolves it. Returns the open DB handle and the repo record.
func setupProfileTestRepo(t *testing.T) (*db.DB, *db.Repo, string) {
	t.Helper()

	repoDir := t.TempDir()
	nmHome := t.TempDir()
	t.Setenv("NM_HOME", nmHome)

	run(t, repoDir, "git", "init")
	run(t, repoDir, "git", "config", "user.email", "test@test.com")
	run(t, repoDir, "git", "config", "user.name", "Test")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "initial")

	rawRoot, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		rawRoot = repoDir
	}
	chdir(t, rawRoot)

	p := paths.WithRoot(nmHome)
	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo, err := database.InsertRepoWithID("repo-1", rawRoot, "git@example.com:team/app.git", "main")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	return database, repo, nmHome
}

// writeTestProfile lays down <nmHome>/profiles/<name>/profile.yaml plus any
// extra files, returning the profile dir.
func writeTestProfile(t *testing.T, nmHome, name, profileYAML string, files map[string]string) string {
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

func repoLocalProfile(t *testing.T, d *db.DB, id string) string {
	t.Helper()
	repo, err := d.GetRepo(id)
	if err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if repo == nil {
		t.Fatal("repo disappeared")
	}
	return repo.LocalProfile
}

func TestProfileUseSetsBinding(t *testing.T) {
	d, repo, nmHome := setupProfileTestRepo(t)
	writeTestProfile(t, nmHome, "team-ios", "version: 1\nsteps:\n  - rebase\n  - review\n  - push\n", nil)

	out, err := executeCmd("profile", "use", "team-ios")
	if err != nil {
		t.Fatalf("profile use: %v\n%s", err, out)
	}
	if !strings.Contains(out, "team-ios") {
		t.Errorf("output should name the bound profile, got %q", out)
	}
	if got := repoLocalProfile(t, d, repo.ID); got != "team-ios" {
		t.Errorf("local profile = %q, want %q", got, "team-ios")
	}
}

// A binding to a missing (or broken) profile is a warning, not a failure: the
// binding is set anyway — the daemon fails closed at run time — but the user
// is told now.
func TestProfileUseWarnsOnMissingProfile(t *testing.T) {
	d, repo, _ := setupProfileTestRepo(t)

	out, err := executeCmd("profile", "use", "ghost")
	if err != nil {
		t.Fatalf("profile use should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("output should carry a warning for a missing profile, got %q", out)
	}
	if got := repoLocalProfile(t, d, repo.ID); got != "ghost" {
		t.Errorf("local profile = %q, want %q (binding set despite warning)", got, "ghost")
	}
}

func TestProfileUseInvalidNameFails(t *testing.T) {
	d, repo, _ := setupProfileTestRepo(t)

	if _, err := executeCmd("profile", "use", "../escape"); err == nil {
		t.Fatal("expected an error for an unsafe profile name")
	}
	if got := repoLocalProfile(t, d, repo.ID); got != "" {
		t.Errorf("local profile = %q, want empty (nothing bound on invalid name)", got)
	}
}

func TestProfileUseClear(t *testing.T) {
	d, repo, nmHome := setupProfileTestRepo(t)
	writeTestProfile(t, nmHome, "team-ios", "steps:\n  - rebase\n  - push\n", nil)
	if _, err := executeCmd("profile", "use", "team-ios"); err != nil {
		t.Fatalf("profile use: %v", err)
	}

	out, err := executeCmd("profile", "use", "--clear")
	if err != nil {
		t.Fatalf("profile use --clear: %v\n%s", err, out)
	}
	if got := repoLocalProfile(t, d, repo.ID); got != "" {
		t.Errorf("local profile = %q, want empty after --clear", got)
	}

	// --clear with a name argument is a usage error.
	if _, err := executeCmd("profile", "use", "--clear", "team-ios"); err == nil {
		t.Fatal("expected an error for --clear with a name argument")
	}
}

func TestProfileUseWithoutNameOrClearFails(t *testing.T) {
	setupProfileTestRepo(t)
	if _, err := executeCmd("profile", "use"); err == nil {
		t.Fatal("expected an error for profile use without a name or --clear")
	}
}

func TestProfileShow(t *testing.T) {
	d, repo, nmHome := setupProfileTestRepo(t)
	writeTestProfile(t, nmHome, "team-local", "steps:\n  - rebase\n  - push\n", nil)

	// No binding, no repo config: nothing selected.
	out, err := executeCmd("profile", "show")
	if err != nil {
		t.Fatalf("profile show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "none") {
		t.Errorf("with nothing selected, output should say none, got %q", out)
	}

	// Repo config selects a profile; no local binding: repo config wins.
	if err := os.WriteFile(filepath.Join(repo.WorkingPath, ".no-mistakes.yaml"), []byte("profile: team-repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = executeCmd("profile", "show")
	if err != nil {
		t.Fatalf("profile show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "team-repo") {
		t.Errorf("output should show the repo-config profile, got %q", out)
	}

	// Local binding set: it wins over the repo-config profile.
	if err := d.SetRepoLocalProfile(repo.ID, "team-local"); err != nil {
		t.Fatal(err)
	}
	out, err = executeCmd("profile", "show")
	if err != nil {
		t.Fatalf("profile show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "team-local") || !strings.Contains(out, "team-repo") {
		t.Errorf("output should show both selections, got %q", out)
	}
	localIdx := strings.Index(out, "team-local")
	if localIdx < 0 || !strings.Contains(out[strings.LastIndex(out, "effective"):], "team-local") {
		t.Errorf("effective profile should be the local binding, got %q", out)
	}
}

func TestProfileList(t *testing.T) {
	_, _, nmHome := setupProfileTestRepo(t)

	// No profiles dir yet.
	out, err := executeCmd("profile", "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "no profiles") {
		t.Errorf("empty list should say no profiles, got %q", out)
	}

	writeTestProfile(t, nmHome, "team-good", "version: 3\nsteps:\n  - rebase\n  - review\n  - push\n", nil)
	writeTestProfile(t, nmHome, "team-broken", "step:\n  - review\n", nil)

	out, err = executeCmd("profile", "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "team-good") || !strings.Contains(out, "team-broken") {
		t.Errorf("list should include both profiles, got %q", out)
	}
	if !strings.Contains(out, "3 steps") {
		t.Errorf("valid profile should show its step count, got %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "invalid") {
		t.Errorf("broken profile should be marked invalid, got %q", out)
	}
}

func TestProfileLintValid(t *testing.T) {
	_, _, nmHome := setupProfileTestRepo(t)
	writeTestProfile(t, nmHome, "team-ios",
		"version: 1\nsteps:\n  - rebase\n  - name: ios-review\n    type: skill\n    skill: skills/review.md\n    mode: review\n    instructions:\n      - instructions/swift.md\n  - push\n",
		map[string]string{
			"skills/review.md":      "Flag force unwraps.",
			"instructions/swift.md": "Prefer guard-let.",
		})

	out, err := executeCmd("profile", "lint", "team-ios")
	if err != nil {
		t.Fatalf("profile lint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("valid profile lint should report ok, got %q", out)
	}
}

func TestProfileLintMissingProfileFails(t *testing.T) {
	setupProfileTestRepo(t)
	if _, err := executeCmd("profile", "lint", "ghost"); err == nil {
		t.Fatal("expected an error for linting a missing profile")
	}
}

// Lint flags a skill path whose file is missing from the profile dir, and an
// escaping path — the same conditions that make the daemon park the step.
func TestProfileLintMissingSkillFileFails(t *testing.T) {
	_, _, nmHome := setupProfileTestRepo(t)
	writeTestProfile(t, nmHome, "team-ios",
		"steps:\n  - name: ios-review\n    type: skill\n    skill: skills/absent.md\n    mode: review\n  - push\n", nil)

	out, err := executeCmd("profile", "lint", "team-ios")
	if err == nil {
		t.Fatalf("expected lint to fail for a missing skill file, got:\n%s", out)
	}
	if !strings.Contains(out+err.Error(), "skills/absent.md") {
		t.Errorf("lint should name the missing file, got %q (%v)", out, err)
	}
}

// `no-mistakes init --profile <name>` sets the host-local binding at init time.
func TestInitProfileFlagSetsBinding(t *testing.T) {
	repoDir := setupTestRepo(t)
	nmHome := os.Getenv("NM_HOME")
	writeTestProfile(t, nmHome, "team-ios", "steps:\n  - rebase\n  - review\n  - push\n", nil)

	out, err := executeCmd("init", "--profile", "team-ios")
	if err != nil {
		t.Fatalf("init --profile: %v\n%s", err, out)
	}
	if !strings.Contains(out, "team-ios") {
		t.Errorf("init output should mention the bound profile, got %q", out)
	}

	p := paths.WithRoot(nmHome)
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	root, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		root = repoDir
	}
	repo, err := d.GetRepoByPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if repo == nil {
		t.Fatal("repo not registered by init")
	}
	if repo.LocalProfile != "team-ios" {
		t.Errorf("local profile = %q, want %q", repo.LocalProfile, "team-ios")
	}
}

// An invalid --profile name fails init before any gate setup happens.
func TestInitProfileFlagInvalidNameFails(t *testing.T) {
	setupProfileTestRepo(t)
	if _, err := executeCmd("init", "--profile", "Bad/Name"); err == nil {
		t.Fatal("expected an error for an invalid --profile name")
	}
	if _, err := executeCmd("init", "--profile", ""); err == nil {
		t.Fatal("expected an error for an empty --profile value")
	}
}

func TestProfileLintInvalidStepsFails(t *testing.T) {
	_, _, nmHome := setupProfileTestRepo(t)
	// Duplicate step name → validateStepSpecs error via BuildPipeline.
	writeTestProfile(t, nmHome, "team-ios", "steps:\n  - review\n  - review\n  - push\n", nil)
	if _, err := executeCmd("profile", "lint", "team-ios"); err == nil {
		t.Fatal("expected lint to fail for an invalid steps list")
	}
}
