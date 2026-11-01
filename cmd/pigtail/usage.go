package main

import "github.com/spf13/cobra"

// silenceUsageOnRunE makes every command in the tree print its error alone
// when the error comes from RunE, while still printing usage when the user
// got the invocation itself wrong.
//
// Why it is done this way. Cobra decides whether to dump usage *after*
// running the command, in ExecuteC:
//
//	err = cmd.execute(flags)
//	if err != nil {
//		...
//		if !cmd.SilenceUsage && !c.SilenceUsage {   // c is the root
//			c.Println(cmd.UsageString())
//		}
//	}
//
// Two things follow. First, the decision is still open while RunE runs, so a
// command can opt out of the usage dump from inside RunE. Second, the gate is
// an AND over the command *and* the root: setting SilenceUsage on the root
// silences usage unconditionally, and a command cannot re-enable it — which
// is why the obvious `root.SilenceUsage = true` is wrong. It also silences
// unknown flags, unparseable flag values, wrong arg counts and flag-group
// violations, all of which are exactly the cases where the user needs to be
// told how to invoke the command. Cobra's SetFlagErrorFunc can claw back the
// flag-parse cases (by un-silencing the *root* from the hook), but there is
// no equivalent hook for cobra.Args validation or ValidateFlagGroups, so
// wrong-arg-count and mutual-exclusion errors stay silenced. Verified
// empirically against cobra v1.10.2.
//
// So the opt-out has to happen at the last possible moment instead. Cobra's
// order inside execute() is: parse flags → validate args → PersistentPreRunE
// → validate required flags → validate flag groups → RunE. RunE is the first
// point past every form of misuse, which makes "set SilenceUsage as the first
// act of RunE" exactly the right rule — and PersistentPreRunE, the other
// tempting single-edit hook, the wrong one, since it runs before the
// required-flag and flag-group checks and would silence those too.
//
// Applying that rule by hand at each of the ~46 RunE sites would work, but it
// is a rule a new command can silently forget. Wrapping the tree once keeps
// the same semantics with no per-command upkeep: any command added to the
// tree before newRootCmd returns inherits the behaviour.
//
// Commands with a Run (rather than RunE) cannot return an error, so there is
// nothing to silence and they are left untouched.
func silenceUsageOnRunE(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			// c is the command cobra is executing, and the same pointer cobra
			// then tests in ExecuteC.
			c.SilenceUsage = true
			return run(c, args)
		}
	}
	for _, sub := range cmd.Commands() {
		silenceUsageOnRunE(sub)
	}
}
