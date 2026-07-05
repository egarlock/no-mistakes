package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/daemon"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"github.com/spf13/cobra"
)

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage shared gate profiles and the current repo's host-local binding",
		Long: `Shared gate profiles live under <NM_HOME>/profiles/<name>/ and define a full
pipeline (steps + skills + instructions) once for many repos.

A repo selects a profile either via the "profile:" field in its trusted
default-branch .no-mistakes.yaml, or via a host-local binding set with
"no-mistakes profile use <name>". The host-local binding wins and requires
ZERO files committed to the repo — ideal for work repos where committing
.no-mistakes.yaml is not an option. It is authored by the machine owner, so
it carries the same trust level as the global config.`,
	}
	cmd.AddCommand(newProfileUseCmd())
	cmd.AddCommand(newProfileShowCmd())
	cmd.AddCommand(newProfileListCmd())
	cmd.AddCommand(newProfileLintCmd())
	return cmd
}

func newProfileUseCmd() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Bind the current repo to a shared gate profile (host-local, nothing committed)",
		Long: `Stores a host-local repo → profile binding in the no-mistakes database. The
bound profile's pipeline gates every run for this repo, overriding the repo
config's "profile:" field, and works even when the repo has no
.no-mistakes.yaml at all.

Use --clear to remove the binding.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, d, err := openResources()
			if err != nil {
				return err
			}
			defer d.Close()

			repo, err := findRepo(d)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()

			if clear {
				if len(args) > 0 {
					return fmt.Errorf("profile use --clear takes no <name> argument")
				}
				if err := d.SetRepoLocalProfile(repo.ID, ""); err != nil {
					return err
				}
				fmt.Fprintf(w, "  %s Cleared host-local profile binding for %s\n", sGreen.Render("✓"), repo.WorkingPath)
				return nil
			}

			if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("profile use requires a <name> argument (or --clear to remove the binding)")
			}
			name := strings.TrimSpace(args[0])
			if !steps.ValidCustomName(name) {
				return fmt.Errorf("invalid profile name %q (use lowercase letters, digits, '-' and '_', starting with a letter or digit)", name)
			}

			// Validate now so the user hears about a missing/broken profile at
			// bind time. This is a warning, not a failure: the binding is
			// stored anyway and the daemon fails closed at run start.
			warnIfProfileUnusable(w, p, name)

			if err := d.SetRepoLocalProfile(repo.ID, name); err != nil {
				return err
			}
			fmt.Fprintf(w, "  %s Bound repo to profile %s\n", sGreen.Render("✓"), sBold.Render(name))
			fmt.Fprintln(w)
			fmt.Fprintf(w, "  %s  %s\n", sDim.Render("   repo"), repo.WorkingPath)
			fmt.Fprintf(w, "  %s  %s\n", sDim.Render("profile"), p.ProfileDir(name))
			fmt.Fprintf(w, "  %s\n", sDim.Render("  This binding is host-local: nothing is committed to the repo, and it"))
			fmt.Fprintf(w, "  %s\n", sDim.Render("  overrides any profile: field in the repo's .no-mistakes.yaml."))
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "clear the host-local profile binding for the current repo")
	return cmd
}

// warnIfProfileUnusable prints a warning when the named profile would fail the
// daemon's fail-closed load at run start (missing dir, unparsable profile.yaml,
// zero steps, revise-mode step, ...).
func warnIfProfileUnusable(w io.Writer, p *paths.Paths, name string) {
	if _, _, err := daemon.LoadProfile(p, name); err != nil {
		fmt.Fprintf(w, "  %s %s\n", sYellow.Render("warning:"), err)
		fmt.Fprintf(w, "  %s\n", sDim.Render("  Runs for this repo will fail at start until the profile is fixed"))
		fmt.Fprintf(w, "  %s\n", sDim.Render("  (the daemon fails closed rather than running the default pipeline)."))
	}
}

func newProfileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the current repo's profile selection and which source wins",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, d, err := openResources()
			if err != nil {
				return err
			}
			defer d.Close()

			repo, err := findRepo(d)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()

			local := strings.TrimSpace(repo.LocalProfile)
			repoProfile := ""
			repoCfgNote := ""
			if repoCfg, cfgErr := config.LoadRepo(repo.WorkingPath); cfgErr != nil {
				repoCfgNote = fmt.Sprintf("unreadable: %v", cfgErr)
			} else {
				repoProfile = strings.TrimSpace(repoCfg.Profile)
			}

			printSelection := func(label, name, note string) {
				value := name
				if value == "" {
					value = "(none)"
				}
				if note != "" {
					value += "  " + sDim.Render(note)
				}
				fmt.Fprintf(w, "  %s  %s\n", sDim.Render(label), value)
			}

			printSelection("    local binding", local, "")
			printSelection("repo config field", repoProfile, repoCfgNote)

			switch {
			case local != "":
				fmt.Fprintf(w, "  %s  %s  %s\n", sDim.Render("        effective"), sBold.Render(local), sDim.Render("(host-local binding wins)"))
			case repoProfile != "":
				fmt.Fprintf(w, "  %s  %s  %s\n", sDim.Render("        effective"), sBold.Render(repoProfile), sDim.Render("(repo config)"))
			default:
				fmt.Fprintf(w, "  %s  %s\n", sDim.Render("        effective"), "(none — default pipeline or repo steps:)")
			}
			fmt.Fprintln(w)
			fmt.Fprintf(w, "  %s\n", sDim.Render("Note: the repo config value shown is the working-tree copy; at run time"))
			fmt.Fprintf(w, "  %s\n", sDim.Render("the daemon enforces the trusted default-branch copy of .no-mistakes.yaml."))

			if local != "" {
				warnIfProfileUnusable(w, p, local)
			}
			return nil
		},
	}
}

func newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List shared gate profiles under <NM_HOME>/profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := paths.New()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			w := cmd.OutOrStdout()

			entries, err := os.ReadDir(p.ProfilesDir())
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("read profiles dir: %w", err)
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
			if len(names) == 0 {
				fmt.Fprintf(w, "  No profiles found under %s\n", p.ProfilesDir())
				return nil
			}
			for _, name := range names {
				profile, _, err := daemon.LoadProfile(p, name)
				if err != nil {
					fmt.Fprintf(w, "  %s %s  %s\n", sYellow.Render("✗ invalid"), sBold.Render(name), sDim.Render(err.Error()))
					continue
				}
				fmt.Fprintf(w, "  %s %s  %s\n", sGreen.Render("✓"), sBold.Render(name),
					sDim.Render(fmt.Sprintf("version %d, %d steps", profile.Version, len(profile.Steps))))
			}
			return nil
		},
	}
}

func newProfileLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint <name>",
		Short: "Validate a shared gate profile with the daemon's run-start rules",
		Long: `Loads and validates <NM_HOME>/profiles/<name>/ exactly the way the daemon does
at run start: profile.yaml must parse (unknown keys rejected), define at least
one step, carry no revise-mode skill steps, and pass the steps validation the
pipeline builder applies. Skill and instruction paths must stay inside the
profile directory and exist on disk.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := paths.New()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			w := cmd.OutOrStdout()
			name := strings.TrimSpace(args[0])

			profile, profileDir, err := daemon.LoadProfile(p, name)
			if err != nil {
				return err
			}

			var issues []string
			// The same step validation BuildPipeline applies to the merged run
			// pipeline (names, ordering invariants, custom-step rules).
			if _, err := steps.BuildPipeline(profile.Steps); err != nil {
				issues = append(issues, err.Error())
			}
			// Skill bodies and instruction files must resolve inside the
			// profile dir and exist — the daemon parks (skills) or drops
			// (instructions) them otherwise.
			for i, spec := range profile.Steps {
				if spec.IsSkill() {
					issues = append(issues, checkProfileFile(profileDir, spec.Skill, fmt.Sprintf("steps[%d] (%s) skill", i, spec.Name))...)
				}
				for _, inst := range spec.Instructions {
					issues = append(issues, checkProfileFile(profileDir, inst, fmt.Sprintf("steps[%d] (%s) instructions", i, spec.Name))...)
				}
			}

			if len(issues) > 0 {
				for _, issue := range issues {
					fmt.Fprintf(w, "  %s %s\n", sYellow.Render("✗"), issue)
				}
				return fmt.Errorf("profile %q has %d lint issue(s)", name, len(issues))
			}
			fmt.Fprintf(w, "  %s Profile %s is ok  %s\n", sGreen.Render("✓"), sBold.Render(name),
				sDim.Render(fmt.Sprintf("(version %d, %d steps)", profile.Version, len(profile.Steps))))
			return nil
		},
	}
}

// checkProfileFile validates that a profile-relative file path stays inside the
// profile dir and exists, using the same containment guard the daemon applies
// (daemon.ProfilePathWithinDir).
func checkProfileFile(profileDir, rel, what string) []string {
	full, ok := daemon.ProfilePathWithinDir(profileDir, rel)
	if !ok {
		return []string{fmt.Sprintf("%s: path %q escapes the profile directory (or is absolute/empty)", what, rel)}
	}
	if _, err := os.Stat(full); err != nil {
		return []string{fmt.Sprintf("%s: %q not found in profile dir: %v", what, rel, err)}
	}
	return nil
}
