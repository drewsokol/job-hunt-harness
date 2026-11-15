package playbooks

import "testing"

// TestHelpText checks every playbook can be listed on the dashboard's
// "Ask your agent" page: it has a phrase to say and text for the user.
func TestHelpText(t *testing.T) {
	for _, p := range All {
		if p.Phrase == "" || p.Help == "" {
			t.Errorf("%s: a playbook needs a Phrase and Help text for the dashboard", p.Name)
		}
	}
}
