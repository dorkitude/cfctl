package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// A local flag with the same name as a root persistent flag (e.g. a
// "--read-only" on a create command) would shadow the global one and could
// silently disable the read-only guard. None of the storage groups may do that.
func TestStorageGroupsDontShadowGlobalFlags(t *testing.T) {
	groups := []*cobra.Command{queuesCmd, hyperdriveCmd, vectorizeCmd, secretsStoreCmd, k2Cmd, basinCmd, artifactsCmd, agentMemoryCmd}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if rootCmd.PersistentFlags().Lookup(f.Name) != nil {
				t.Errorf("%s defines --%s, shadowing the global flag", c.CommandPath(), f.Name)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	for _, g := range groups {
		walk(g)
	}
}
