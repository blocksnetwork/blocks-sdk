package cmd

import (
	"fmt"
	"sort"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(profileCmd)
	profileCmd.AddCommand(profileListCmd, profileUseCmd, profileRenameCmd, profileRemoveCmd)
}

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage deployment profiles (Blocks Network / Enterprise instances)",
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := profiles.Load()
		if err != nil {
			return err
		}
		names := make([]string, 0, len(c.Profiles))
		for n := range c.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			marker := "  "
			if n == c.Active {
				marker = "* "
			}
			p := c.Profiles[n]
			target := p.BaseURL
			if target == "" {
				target = "Blocks Network (default)"
			}
			fmt.Printf("%s%s  %s\n", marker, n, target)
		}
		return nil
	},
}

var profileUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Switch the active profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _, err := profiles.SetActive(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("Active profile: %s\n", name)
		return nil
	},
}

var profileRenameCmd = &cobra.Command{
	Use:   "rename <old> <new>",
	Short: "Rename a profile",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := profiles.Rename(args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("Renamed profile: %s -> %s\n", args[0], args[1])
		return nil
	},
}

var profileRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := profiles.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed profile: %s\n", args[0])
		return nil
	},
}
