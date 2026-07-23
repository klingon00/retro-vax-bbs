package lobby

import "testing"

func TestBuildWelcome_ProviderPipeline(t *testing.T) {
	const version = "v1.2.3"
	greeting := "Welcome, alice. Type HELP for a list of commands."
	versionLine := "%VAX-BBS-I-VERSION, running v1.2.3"

	t.Run("non-admin sees greeting + version only", func(t *testing.T) {
		got := buildWelcome("alice", "user", version, nil)
		assertBannerLines(t, got, []string{greeting, versionLine})
	})

	t.Run("admin with empty queue sees no pending lines", func(t *testing.T) {
		db := newTestLobbyStore(t)
		got := buildWelcome("alice", "admin", version, db)
		assertBannerLines(t, got, []string{greeting, versionLine})
	})

	t.Run("admin with pending sees the two-line pending block appended", func(t *testing.T) {
		db := newTestLobbyStore(t)
		if _, err := db.CreatePendingAccount("bob", "", "x", 0); err != nil {
			t.Fatalf("seeding pending account: %v", err)
		}
		got := buildWelcome("alice", "admin", version, db)
		assertBannerLines(t, got, []string{
			greeting,
			versionLine,
			"%VAX-BBS-I-PEND, 1 account registration(s) awaiting approval.",
			"  Type LIST PENDING to review.",
		})
	})
}

func assertBannerLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("line count: got %d %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n  got  %q\n  want %q", i, got[i], want[i])
		}
	}
}
