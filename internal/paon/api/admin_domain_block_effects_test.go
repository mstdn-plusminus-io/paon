package api

import (
	"database/sql"
	"os"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminDomainBlockAccountEffectModes(t *testing.T) {
	tests := []struct {
		name        string
		severity    string
		wantSilence bool
		wantSuspend bool
	}{
		{name: "silence", severity: "silence", wantSilence: true},
		{name: "suspend", severity: "suspend", wantSuspend: true},
		{name: "noop", severity: "noop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := models.DomainBlock{Severity: domainBlockSeverityValue(tt.severity)}
			if got := adminDomainBlockSilencesAccounts(block); got != tt.wantSilence {
				t.Fatalf("silence = %v, want %v", got, tt.wantSilence)
			}
			if got := adminDomainBlockSuspendsAccounts(block); got != tt.wantSuspend {
				t.Fatalf("suspend = %v, want %v", got, tt.wantSuspend)
			}
			if got := adminDomainBlockNoop(block); got != (tt.severity == "noop") {
				t.Fatalf("noop = %v", got)
			}
		})
	}
}

func TestAdminDomainBlockMediaEffectsMatchMastodon44(t *testing.T) {
	src, err := os.ReadFile("admin_domain_block_effects.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if block.RejectMedia {`,
		`return s.clearDomainMediaCache(database, block.Domain)`,
		`if adminDomainBlockSuspendsAccounts(block) {`,
		`return s.deleteDomainCustomEmojis(database, block.Domain)`,
	} {
		if !functionBodyContains(t, src, "applyAdminDomainBlockMediaEffects", want) {
			t.Fatalf("domain-block media effects missing %q", want)
		}
	}
	for _, fn := range []string{"enqueueAdminDomainBlockEffectsOrApply"} {
		if !functionBodyContains(t, src, fn, `adminDomainBlockSuspendsAccounts(block) || block.RejectMedia`) ||
			!functionBodyContains(t, src, fn, `s.applyAdminDomainBlockMediaEffects(database, block)`) {
			t.Fatalf("%s does not schedule/fallback custom emoji purge for every suspension", fn)
		}
	}

	worker, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"handleAsynqDomainBlock", "handleAsynqDomainClearMedia"} {
		if !functionBodyContains(t, worker, fn, `adminDomainBlockSuspendsAccounts(block)`) ||
			!functionBodyContains(t, worker, fn, `s.applyAdminDomainBlockMediaEffects`) {
			t.Fatalf("%s does not retain the 4.4 suspension emoji purge contract", fn)
		}
	}
}

func TestAdminDomainBlockSurfacesApplyAccountEffects(t *testing.T) {
	files := map[string]map[string]string{
		"admin_blocks.go": {
			"createAdminDomainBlock": `s.enqueueAdminDomainBlockEffectsOrApply(s.db, row, false)`,
			"updateAdminDomainBlock": `s.enqueueAdminDomainBlockEffectsOrApply(s.db, row, true)`,
			"deleteAdminDomainBlock": `s.applyAdminDomainUnblockEffects(s.db, row)`,
			"deleteAdminDomainAllow": `s.applyAdminDomainUnallowEffects(s.db, row.Domain)`,
		},
		"admin_domain_blocks_web.go": {
			"createAdminDomainBlockWeb":      `s.enqueueAdminDomainBlockEffectsOrApply(s.db, block, false)`,
			"updateAdminDomainBlockWeb":      `s.enqueueAdminDomainBlockEffectsOrApply(s.db, block, true)`,
			"destroyAdminDomainBlockWeb":     `s.applyAdminDomainUnblockEffects(s.db, block)`,
			"createAdminDomainBlockFromForm": `s.enqueueAdminDomainBlockEffectsOrApply(s.db, block, false)`,
		},
		"admin_domain_allows_web.go": {
			"destroyAdminDomainAllowWeb": `s.applyAdminDomainUnallowEffects(s.db, row.Domain)`,
		},
	}
	for file, checks := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for fn, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s:%s does not contain %q", file, fn, want)
			}
		}
	}
}

func TestAdminDomainUnallowLimitedFederationSuspendsAndPurgesExactDomain(t *testing.T) {
	src, err := os.ReadFile("admin_domain_block_effects.go")
	if err != nil {
		t.Fatal(err)
	}
	bodyChecks := map[string][]string{
		"applyAdminDomainUnallowEffects": {
			`!s.cfg.LimitedFederationMode`,
			`Where("lower(domain) = ?", domain)`,
			`Where("suspended_at IS NULL")`,
			`"suspended_at":      now`,
			`s.enqueueAfterUnallowDomainOrRun(context.Background(), database, domain)`,
		},
		"runAfterUnallowDomainEffects": {
			`Where("lower(domain) = ?", domain)`,
			`s.purgeAdminUnallowedAccountIDs(database.WithContext(ctx), accountIDs)`,
		},
		"purgeAdminDomainSuspendedAccounts": {
			`s.purgeAdminSuspendedAccountIDs(database, accountIDs)`,
		},
	}
	for fn, checks := range bodyChecks {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
}

func TestAdminDomainSuspendCleanupKeepsRailsSafeAssociationPurge(t *testing.T) {
	src, err := os.ReadFile("admin_domain_block_effects.go")
	if err != nil {
		t.Fatal(err)
	}
	bodyChecks := map[string][]string{
		"applyAdminDomainBlockEffects": {
			`s.purgeAdminDomainSuspendedAccounts(database, domain, block.CreatedAt)`,
		},
		"purgeAdminSuspendedAccountIDs": {
			`return s.purgeAdminSuspendedAccountIDsWithMode(database, accountIDs, false)`,
		},
		"purgeAdminUnallowedAccountIDs": {
			`return s.purgeAdminSuspendedAccountIDsWithMode(database, accountIDs, true)`,
		},
		"purgeAdminSuspendedAccountIDsWithMode": {
			`s.adminSuspendedRemoteFollowDeliveries(database, accountIDs)`,
			`s.clearAdminDomainSuspendedAccountLocalFiles(database, accountIDs, now)`,
			`if !destroyRows {`,
			`s.clearAdminSuspendedAccountFeedCaches(context.Background(), database, accountIDs)`,
			`s.tombstoneAdminDomainSuspendedAccountStatuses(database, accountIDs, now)`,
			`s.deliverAdminSuspendedRemoteFollowActivities(followDeliveries)`,
			`s.applyAdminSuspendedRemoteFollowCacheEffects(context.Background(), followDeliveries)`,
			`if destroyRows {`,
			`database.Where("id IN (?)", accountIDs).Delete(&models.Account{}).Error`,
			`s.enqueueFASPAccountLifecycle(context.Background(), account, "delete")`,
			`s.enqueueFASPAccountLifecycleUpdate(context.Background(), previous, current)`,
		},
		"adminSuspendedRemoteFollowDeliveries": {
			`Where("follows.account_id IN (?)", accountIDs)`,
			`Where("follows.target_account_id IN (?)", accountIDs)`,
			`Kind:      "Reject"`,
			`Kind:      "Undo"`,
			`Local:     follow.TargetAccount`,
			`Remote:    follow.Account`,
			`Local:     follow.Account`,
			`Remote:    follow.TargetAccount`,
		},
		"deliverAdminSuspendedRemoteFollowActivities": {
			`s.deliverActivityPubFollowResponse("Reject", delivery.Local, delivery.Remote, delivery.FollowID, delivery.FollowURI)`,
			`s.deliverActivityPubUndoFollow(delivery.Local, delivery.Remote, delivery.FollowID, delivery.FollowURI)`,
		},
		"deliverAdminAccountDeletionActivities": {
			`if account.Local()`,
			`return s.deliverActivityPubAccountDelete(account)`,
			`if account.Protocol != 1`,
			`s.adminSuspendedRemoteFollowDeliveries(database, adminSingleAccountIDSubquery(database, account.ID))`,
			`s.deliverAdminSuspendedRemoteFollowActivities(deliveries)`,
		},
		"applyAdminSuspensionWorkerEffects": {
			`database.Where("id = ?", accountID).First(&account)`,
			`adminSuspensionRejectsRemoteFollows(account)`,
			`s.adminSuspensionWorkerRejectDeliveries(database, accountID)`,
			`deleteFollow(tx, models.Follow{`,
			`tx.Where("account_id = ?", account.ID).Delete(&models.StatusTrend{})`,
			`s.deliverAdminSuspendedRemoteFollowActivities(deliveries)`,
			`s.applyAdminSuspendedRemoteFollowCacheEffects(ctx, deliveries)`,
			`s.applyAdminAccountMediaVisibility(ctx, database, account.ID, true)`,
			`_ = s.deliverActivityPubAccountUpdate(account)`,
			`s.applyAdminSuspensionFeedUnmerge(ctx, database, account)`,
			`s.clearAdminSuspendedAccountFeedCaches(ctx, database, accountIDs)`,
		},
		"adminSuspensionWorkerRejectDeliveries": {
			`Where("follows.account_id = ?", accountID)`,
			`Kind:      "Reject"`,
			`Local:     follow.TargetAccount`,
			`Remote:    follow.Account`,
		},
		"applyAdminSuspendedRemoteFollowCacheEffects": {
			`s.invalidateFollowRelationshipCaches(ctx, delivery.Remote, delivery.Local.ID)`,
			`s.invalidateFollowRelationshipCaches(ctx, delivery.Local, delivery.Remote.ID)`,
			`s.unmergeAfterUnfollowBestEffort(ctx, delivery.Remote.ID, delivery.Local)`,
			`s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, ids...)`,
		},
		"applyAdminUnsuspensionWorkerEffects": {
			`database.Where("id = ?", accountID).First(&account)`,
			`s.applyAdminAccountMediaVisibility(context.Background(), database, account.ID, false)`,
			`s.applyAdminUnsuspensionFeedMerge(context.Background(), database, account)`,
			`_ = s.deliverActivityPubAccountUpdate(account)`,
			`Updates(map[string]any{"last_webfingered_at": nil})`,
			`s.fetchAndStoreActivityActorForAcct(account.Acct())`,
			`s.enqueueRefollowTask(account.ID)`,
			`s.applyAdminUnsuspensionFeedMerge(context.Background(), database, account)`,
		},
		"applyAdminAccountMediaVisibility": {
			`Where("account_id = ?", accountID)`,
			`Where("file_file_name IS NOT NULL OR thumbnail_file_name IS NOT NULL")`,
			`s.applyMediaAttachmentVisibility(ctx, attachment, private)`,
		},
		"applyAdminSuspensionFeedUnmerge": {
			`JOIN follows ON follows.account_id = accounts.id`,
			`JOIN users ON users.account_id = accounts.id`,
			`Where("follows.target_account_id = ?", account.ID)`,
			`Where("(accounts.domain IS NULL OR accounts.domain = '')")`,
			`s.unmergeAccountFromHomeFeed(ctx, database, account.ID, follower)`,
			`adminAccountListsForLocalDistribution(ctx, database, account.ID)`,
			`s.unmergeAccountFromListFeed(ctx, database, account.ID, list)`,
		},
		"applyAdminUnsuspensionFeedMerge": {
			`JOIN follows ON follows.account_id = accounts.id`,
			`JOIN users ON users.account_id = accounts.id`,
			`Where("follows.target_account_id = ?", account.ID)`,
			`Where("(accounts.domain IS NULL OR accounts.domain = '')")`,
			`s.mergeAccountIntoHomeFeed(ctx, database, account.ID, follower)`,
			`adminAccountListsForLocalDistribution(ctx, database, account.ID)`,
			`s.mergeAccountIntoListFeed(ctx, database, account.ID, list)`,
		},
		"adminAccountListsForLocalDistribution": {
			`JOIN users ON users.account_id = lists.account_id`,
			`JOIN list_accounts ON list_accounts.list_id = lists.id`,
			`Where("list_accounts.account_id = ?", accountID)`,
			`Where("(list_accounts.follow_id IS NOT NULL OR lists.account_id = ?)", accountID)`,
		},
		"clearAdminDomainSuspendedAccountLocalFiles": {
			`s.removeAccountImageObjects(account)`,
			`s.removeAccountLocalImageFiles(account.ID)`,
			`s.removeMediaAttachmentLocalFiles(attachment)`,
			`clearMediaAttachmentFileUpdates(now)`,
		},
		"clearAdminSuspendedAccountFeedCaches": {
			`JOIN users ON users.account_id = follows.account_id`,
			`follows.target_account_id IN (?)`,
			`s.clearHomeFeedCacheContext(ctx, accountID)`,
			`JOIN list_accounts ON list_accounts.list_id = lists.id`,
			`list_accounts.account_id IN (?)`,
			`s.clearListFeedCacheContext(ctx, listID)`,
		},
		"purgeAdminDomainSuspendedAccountAssociations": {
			`"account_notes"`,
			`"account_pins"`,
			`"account_domain_blocks"`,
			`"conversation_mutes"`,
			`"featured_tags"`,
			`"list_accounts"`,
			`"scheduled_statuses"`,
			`"status_pins"`,
			`"tag_follows"`,
			`"follows"`,
			`"follow_requests"`,
			`"blocks"`,
			`"mutes"`,
			`DELETE FROM notifications`,
			`UPDATE status_stats`,
			`DELETE FROM favourites`,
			`DELETE FROM bookmarks`,
			`recalculateRelationshipCountersForAccountIDs(database, affectedRelationshipAccountIDs, now)`,
		},
		"tombstoneAdminDomainSuspendedAccountStatuses": {
			`unlinkDirectStatusesFromConversationsForQuery(context.Background(), database, statusIDs, now)`,
			`Updates(map[string]any{"deleted_at": now, "updated_at": now})`,
			`DELETE FROM status_pins WHERE status_id IN (?) OR status_id IN (?)`,
			`recalculateStatusCountersForStatusIDs(database, affectedStatusIDs, now)`,
			`s.publishBatchedAccountDeletionStatusDeletesForQuery(ctx, database, statusIDs, now)`,
			`s.publishBatchedAccountDeletionStatusDeletesForQuery(ctx, database, reblogIDs, now)`,
		},
	}
	for fn, wants := range bodyChecks {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s does not contain %q", fn, want)
			}
		}
	}
	instancesSrc, err := os.ReadFile("admin_instances.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, want := range map[string]string{
		"recalculateRelationshipCountersForAccountIDs": `WHERE account_id IN ?`,
		"recalculateStatusCountersForStatusIDs":        `WHERE status_id IN ?`,
	} {
		if !functionBodyContains(t, instancesSrc, fn, want) {
			t.Fatalf("%s must scope its indexed recount with %q", fn, want)
		}
	}
}

func TestAdminSuspensionRejectRemoteFollowPolicy(t *testing.T) {
	local := models.Account{Protocol: 1}
	if adminSuspensionRejectsRemoteFollows(local) {
		t.Fatal("local suspension must not reject follows")
	}
	remote := models.Account{Domain: sql.NullString{String: "remote.example", Valid: true}, Protocol: 1}
	if !adminSuspensionRejectsRemoteFollows(remote) {
		t.Fatal("locally-initiated remote suspension must reject remote follows")
	}
	remote.SuspensionOrigin = sql.NullInt64{Int64: 1, Valid: true}
	if adminSuspensionRejectsRemoteFollows(remote) {
		t.Fatal("remote-origin suspension must preserve remote follows")
	}
}
