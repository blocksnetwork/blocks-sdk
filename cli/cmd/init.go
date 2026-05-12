package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/scaffold"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

var (
	initYes      bool
	initLanguage string
	initType     string
)

func init() {
	initCmd.Flags().BoolVarP(&initYes, "yes", "y", false, "Use defaults (non-interactive)")
	initCmd.Flags().StringVarP(&initLanguage, "language", "l", "", "Project language: node or python (default: python)")
	initCmd.Flags().StringVarP(&initType, "type", "t", "", "Project type: provider or consumer (default: provider)")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Scaffold a new agent or consumer project",
	Long: `Create a new Blocks project.

  --type provider (default): an agent handler project with handler.{ts,py},
    trigger.{ts,py}, and agent-card.json. Deploy with 'blocks publish' and
    run with 'blocks run'.

  --type consumer: a script that calls other agents via TaskClient. Produces
    index.ts (Node) or main.py (Python). Run directly (npm run start /
    python main.py) after setting BLOCKS_API_KEY in .env.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var nameFromArgs string
		if len(args) > 0 {
			nameFromArgs = args[0]
		}

		if initType != "" && initType != "provider" && initType != "consumer" {
			return fmt.Errorf("unsupported type %q (use \"provider\" or \"consumer\")", initType)
		}

		// Determine if we should run non-interactively.
		nonInteractive := initYes || !isInteractive()

		var cfg wizard.Config
		if nonInteractive {
			if nameFromArgs == "" {
				return fmt.Errorf("agent name is required in non-interactive mode\nUsage: blocks init <name> --yes")
			}
			if err := wizard.ValidateAgentName(nameFromArgs); err != nil {
				return err
			}
			cfg = wizard.DefaultConfig(nameFromArgs)
			if initLanguage == "node" || initLanguage == "python" {
				cfg.Language = initLanguage
			} else if initLanguage != "" {
				return fmt.Errorf("unsupported language %q (use \"node\" or \"python\")", initLanguage)
			}
			if initType == "consumer" {
				cfg.Type = "consumer"
				// DefaultConfig seeds Description with "<name> agent"; for
				// consumers the interactive wizard rewrites it to "<name>
				// consumer" (wizard.go), so mirror that here or the leak
				// shows up in the scaffolded pyproject.toml.
				cfg.Description = cfg.Name + " consumer"
			}
		} else {
			// Validate --language flag before starting the wizard
			if initLanguage != "" && initLanguage != "node" && initLanguage != "python" {
				return fmt.Errorf("unsupported language %q (use \"node\" or \"python\")", initLanguage)
			}
			var err error
			cfg, err = wizard.Run(nameFromArgs, initLanguage, initType)
			if err != nil {
				return err
			}
		}

		dir := filepath.Join(mustCwd(), cfg.Name)
		if _, err := os.Stat(dir); err == nil {
			return fmt.Errorf("directory %q already exists", cfg.Name)
		}

		if !nonInteractive {
			fmt.Printf("\n  This will create ./%s/ with your %s project files.\n", cfg.Name, cfg.Language)
			fmt.Print("  Continue? (Y/n): ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
				if ans == "n" || ans == "no" {
					return fmt.Errorf("canceled")
				}
			}
		}

		if err := scaffold.Project(dir, cfg); err != nil {
			return fmt.Errorf("scaffold failed: %w", err)
		}

		printNextSteps(cfg)
		return nil
	},
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func printNextSteps(cfg wizard.Config) {
	lang := "Python"
	if cfg.Language == "node" {
		lang = "Node"
	}

	role := "agent"
	if cfg.Type == "consumer" {
		role = "consumer"
	}

	fmt.Printf("\n  %s %s '%s' created!\n\n", lang, role, cfg.Name)
	fmt.Println("  Next steps:")
	fmt.Printf("    cd %s\n", cfg.Name)

	if cfg.Language == "node" {
		fmt.Println("    npm install")
	} else {
		fmt.Println("    pip install -e .")
	}

	if cfg.Type == "consumer" {
		fmt.Println("    # 1. Set BLOCKS_API_KEY in .env  (or run 'blocks login --write-env')")
		fmt.Println("    # 2. Edit the script and set the target agent name")
		if cfg.Language == "node" {
			fmt.Println("    npm run start")
		} else {
			fmt.Println("    python main.py")
		}
		return
	}

	fmt.Println("    blocks login --write-env  # authenticate (first time only)")
	fmt.Println("    blocks publish            # publish to registry")
	fmt.Println("    blocks run                # start the agent")
}
