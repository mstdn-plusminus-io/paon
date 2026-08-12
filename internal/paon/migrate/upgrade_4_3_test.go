package migrate

import (
	"database/sql"
	"slices"
	"sort"
	"testing"
)

func TestMastodon43UpgradeInventoryRecordsEveryUpstreamMigration(t *testing.T) {
	versions := []string{
		"20240109103012",
		"20240304090449",
		"20240307180905",
		"20240321160706",
		"20240603195202",
		"20240808124338",
		"20240808124339",
	}
	for _, steps := range [][]upgradeStep{mastodon43ExpandSteps(), mastodon43DuplicateBackfillSteps(), mastodon43ValidateSteps(), mastodon43ContractSteps()} {
		for _, step := range steps {
			versions = append(versions, step.version)
		}
	}
	sort.Strings(versions)
	want := []string{
		"20231006183200", "20231018192110", "20231018193209", "20231018193355", "20231018193659",
		"20231210154528", "20231211234923", "20231212073317", "20231222100226",
		"20240109103012", "20240111033014",
		"20240217171534", "20240221195424", "20240221195828", "20240221211359", "20240222193403", "20240222203722", "20240227191620",
		"20240304090449", "20240307180905", "20240310123453", "20240312100644", "20240312105620", "20240320140159", "20240320163441", "20240321160706", "20240322125607", "20240322130318", "20240322161611",
		"20240510192043", "20240513095755", "20240513123807", "20240522041528",
		"20240603195202", "20240607093446", "20240607093954", "20240607094603", "20240607094856",
		"20240712064044", "20240713171841", "20240713171909", "20240720140205", "20240724181224",
		"20240808114841", "20240808124338", "20240808124339", "20240808125420",
		"20240909014637", "20240916190140", "20241007071624",
	}
	if !slices.Equal(versions, want) {
		t.Fatalf("migration versions = %#v\nwant %#v", versions, want)
	}
}

func TestNotificationPolicyFromLegacySettings(t *testing.T) {
	settings := sql.NullString{Valid: true, String: `{"interactions.must_be_follower":true,"interactions.must_be_following":true,"interactions.must_be_following_dm":false}`}
	policy, required, err := notificationPolicyFromSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !required || !policy.FilterNotFollowers || !policy.FilterNotFollowing || policy.FilterPrivateMentions {
		t.Fatalf("policy = %#v required=%v", policy, required)
	}

	defaults := sql.NullString{Valid: true, String: `{"interactions.must_be_follower":false,"interactions.must_be_following":false,"interactions.must_be_following_dm":true}`}
	policy, required, err = notificationPolicyFromSettings(defaults)
	if err != nil || required || policy.FilterNotFollowers || policy.FilterNotFollowing || !policy.FilterPrivateMentions {
		t.Fatalf("default policy = %#v required=%v err=%v", policy, required, err)
	}
}

func TestMigrationTOTPCodeVerifiesNormalizedSecret(t *testing.T) {
	if !sameMigrationTOTPCode("jbsw y3dp ehpk3pxp==", "JBSWY3DPEHPK3PXP") {
		t.Fatal("equivalent OTP secrets produced different migration verification codes")
	}
	if sameMigrationTOTPCode("JBSWY3DPEHPK3PXP", "KRUGS4ZANFZSAYJA") {
		t.Fatal("different OTP secrets produced an accepted migration verification code")
	}
}

func TestOptionsFromEnvUsesExplicitMigrationGates(t *testing.T) {
	t.Setenv("PAON_MIGRATION_PHASE", "backfill")
	t.Setenv("PAON_MIGRATION_ACKNOWLEDGE_CONTRACT", "true")
	t.Setenv("MIGRATION_IGNORE_INVALID_OTP_SECRET", "true")
	t.Setenv("MIGRATION_SKIP_TAG_TREND_BACKFILL", "true")
	t.Setenv("OTP_SECRET", "legacy")
	t.Setenv("ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY", "primary")
	t.Setenv("ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY", "deterministic")
	t.Setenv("ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT", "salt")
	options := OptionsFromEnv()
	if options.Phase != UpgradePhaseBackfill || !options.AcknowledgeContract || !options.IgnoreInvalidOTPSecret || !options.Mastodon44SkipTagTrendBackfill || options.OTPSecret != "legacy" {
		t.Fatalf("options = %#v", options)
	}
	if options.ActiveRecordEncryption.PrimaryKey != "primary" || options.ActiveRecordEncryption.DeterministicKey != "deterministic" || options.ActiveRecordEncryption.KeyDerivationSalt != "salt" {
		t.Fatalf("Active Record credentials were not loaded")
	}
}

func TestRequestedUpgradePhaseDefaultsSafelyAndPreservesContractCLI(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    UpgradePhase
		wantErr bool
	}{
		{name: "safe default", want: UpgradePhaseExpand},
		{name: "explicit backfill", options: Options{Phase: " BACKFILL "}, want: UpgradePhaseBackfill},
		{name: "explicit validate", options: Options{Phase: UpgradePhaseValidate}, want: UpgradePhaseValidate},
		{name: "explicit contract requires later acknowledgement check", options: Options{Phase: UpgradePhaseContract}, want: UpgradePhaseContract},
		{name: "acknowledgement is only a gate", options: Options{AcknowledgeContract: true}, want: UpgradePhaseExpand},
		{name: "invalid", options: Options{Phase: "all"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := requestedUpgradePhase(test.options)
			if (err != nil) != test.wantErr {
				t.Fatalf("requestedUpgradePhase() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("requestedUpgradePhase() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUpgradePhasesThroughUsesSeparateOrderedStages(t *testing.T) {
	want := []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract}
	got := upgradePhasesThrough(UpgradePhaseContract)
	if !slices.Equal(got, want) {
		t.Fatalf("upgradePhasesThrough(contract) = %#v, want %#v", got, want)
	}
	if got := upgradePhasesThrough(UpgradePhaseExpand); !slices.Equal(got, want[:1]) {
		t.Fatalf("upgradePhasesThrough(expand) = %#v, want %#v", got, want[:1])
	}
}

func TestMastodon43UpgradeVersionKnownRejectsUnknownResumeMarkers(t *testing.T) {
	for _, phase := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract} {
		for _, version := range mastodon43PhaseVersions(phase) {
			if !mastodon43UpgradeVersionKnown(version) {
				t.Fatalf("known phase %s version %s was rejected", phase, version)
			}
		}
	}
	if mastodon43UpgradeVersionKnown("20231111111111") {
		t.Fatal("unknown migration marker was accepted")
	}
}
