//go:build upgrade_v69 && offline_upgrade && upgrade_target

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"maps"
	"path/filepath"
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/app/migration"
	paramstypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/params/types"
	upgradetypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/upgrade/types"
	abci "github.com/sei-protocol/sei-chain/sei-tendermint/abci/types"
	tmproto "github.com/sei-protocol/sei-chain/sei-tendermint/proto/tendermint/types"
	"github.com/stretchr/testify/require"
)

const (
	v69OfflineUpgradeName = "v6.9"
	// v69OfflineParamsVersion is the params consensus version after the upgrade.
	v69OfflineParamsVersion uint64 = 3
)

var (
	v69OfflineDisabledMsgTypeURLsKey = []byte(paramstypes.ModuleName + "/" + string(paramstypes.ParamStoreKeyDisabledMsgTypeURLs))
	v69OfflineMigrationRateKey       = []byte(migration.SubspaceName + "/" + string(migration.KeyNumKeysToMigratePerBlock))
	v69OfflineUpgradeBlockTime       = time.Unix(1_700_000_000, 0).UTC()
)

func TestV69OfflineUpgradeTarget(t *testing.T) {
	t.Run("fixture", testV69OfflineUpgradeTargetFixture)
	t.Run("snapshot", testV69OfflineUpgradeTargetSnapshot)
}

func testV69OfflineUpgradeTargetFixture(t *testing.T) {
	root := requireOfflineUpgradePhase(t, "target")
	artifact := readOfflineUpgradeArtifact(t, root)
	require.Equal(t, v69OfflineUpgradeName, artifact.Upgrade)
	require.Equal(t, artifact.SourceHeight+1, artifact.UpgradeHeight)

	t.Setenv("UPGRADE_VERSION_LIST", LatestUpgrade)

	cleanRoot := filepath.Join(root, offlineUpgradeMigratedDir)
	copyOfflineUpgradeDatabase(t, root, cleanRoot)
	testApp := openOfflineUpgradeApp(t, cleanRoot, false)
	plan, found := committedOfflineUpgradePlan(t, testApp)
	require.True(t, found, "committed upgrade plan did not survive the process boundary")
	require.Equal(t, artifact.Upgrade, plan.Name)
	require.Equal(t, artifact.UpgradeHeight, plan.Height)
	require.True(t, testApp.UpgradeKeeper.HasHandler(plan.Name),
		"target binary has no handler for persisted plan name %q", plan.Name)
	finalizeV69OfflineUpgrade(t, testApp, artifact.UpgradeHeight)
	commitOfflineUpgradeApp(t, testApp)
	closeOfflineUpgradeApp(t, testApp)

	reopened := openOfflineUpgradeApp(t, cleanRoot, false)
	require.Equal(t, artifact.UpgradeHeight, reopened.LastBlockHeight())
	requireV69OfflineMigrated(t, reopened, artifact)
	hash := committedOfflineUpgradeHash(t, reopened)
	closeOfflineUpgradeApp(t, reopened)

	artifact.MigratedRoot = offlineUpgradeMigratedDir
	artifact.UpgradeHash = offlineUpgradeHashString(hash)
	writeOfflineUpgradeArtifact(t, root, artifact)
}

// requireV69OfflineMigrated checks a reopened migrated database: v6.9 is recorded as applied,
// params is at consensus version 3 with the disabled message type URLs stored as an empty list,
// and the module version map and the params and upgrade stores changed nowhere else.
func requireV69OfflineMigrated(t *testing.T, testApp *App, artifact offlineUpgradeArtifact) {
	t.Helper()
	ctx := offlineUpgradeReadContext(testApp, testApp.LastBlockHeight())
	lastName, lastHeight := testApp.UpgradeKeeper.GetLastCompletedUpgrade(ctx)
	require.Equal(t, artifact.Upgrade, lastName)
	require.Equal(t, artifact.UpgradeHeight, lastHeight)
	require.Equal(t, artifact.ModuleVersions, offlineUpgradeModuleVersions(t, testApp))
	require.Equal(t, v69OfflineParamsVersion, testApp.UpgradeKeeper.GetModuleVersionMap(ctx)[paramstypes.ModuleName])
	require.Empty(t, testApp.ParamsKeeper.GetDisabledMsgTypeURLs(ctx))

	wantParams := maps.Clone(artifact.Stores[paramstypes.StoreKey])
	wantParams[encodeOfflineUpgradeKey(v69OfflineDisabledMsgTypeURLsKey)] = base64.StdEncoding.EncodeToString([]byte("[]"))
	// BeginBlock stores the migration rate the first time it finds it unset. The source
	// phase commits without running a block, so here that is the upgrade block.
	wantParams[encodeOfflineUpgradeKey(v69OfflineMigrationRateKey)] = base64.StdEncoding.EncodeToString([]byte(`"0"`))
	require.Equal(t, wantParams, snapshotCommittedOfflineUpgradeStore(t, testApp, paramstypes.StoreKey),
		"v6.9 changed the params store beyond storing the disabled message type URLs")
	requireOfflineUpgradeRetainedStoresExcept(t, testApp, artifact.Stores, v69OfflineTouchedKey)
}

// v69OfflineTouchedKey reports the retained-store keys the v6.9 upgrade block is specified to
// change: the plan and done entries of the upgrade store and the params entry of its module
// version map.
func v69OfflineTouchedKey(storeName string, key []byte) bool {
	if storeName != upgradetypes.StoreKey {
		return false
	}
	return bytes.Equal(key, offlineUpgradeModuleVersionKey(paramstypes.ModuleName)) ||
		(len(key) > 0 && (key[0] == upgradetypes.PlanByte || key[0] == upgradetypes.DoneByte))
}

func finalizeV69OfflineUpgrade(t *testing.T, testApp *App, height int64) {
	t.Helper()
	_, err := testApp.FinalizeBlock(context.Background(), &abci.RequestFinalizeBlock{
		Hash: []byte("offline-upgrade"),
		Header: &tmproto.Header{
			ChainID: offlineUpgradeChainID, Height: height, Time: v69OfflineUpgradeBlockTime,
		},
	})
	require.NoError(t, err)
}

// testV69OfflineUpgradeTargetSnapshot applies v6.9 to the node home named by
// UPGRADE_TEST_SNAPSHOT_HOME and requires params to move to consensus version 3, nothing else
// in the module version map to change, and the disabled message type URLs to be stored as an
// empty list.
func testV69OfflineUpgradeTargetSnapshot(t *testing.T) {
	home := requireOfflineUpgradeSnapshotHome(t)
	t.Setenv("UPGRADE_VERSION_LIST", v69OfflineUpgradeName)
	chainID := readOfflineUpgradeGenesisChainID(t, home)

	testApp := openOfflineUpgradeSnapshotApp(t, home, chainID)
	sourceHeight := testApp.LastBlockHeight()
	wantVersions := testApp.UpgradeKeeper.GetModuleVersionMap(offlineUpgradeContext(testApp, sourceHeight, chainID))
	wantVersions[paramstypes.ModuleName] = v69OfflineParamsVersion
	upgradeHeight := sourceHeight + 1
	testApp.UpgradeKeeper.ApplyUpgrade(offlineUpgradeContext(testApp, upgradeHeight, chainID), upgradetypes.Plan{
		Name:   v69OfflineUpgradeName,
		Height: upgradeHeight,
	})
	testApp.CommitMultiStore().Commit(true)
	closeOfflineUpgradeApp(t, testApp)

	reopened := openOfflineUpgradeSnapshotApp(t, home, chainID)
	defer closeOfflineUpgradeApp(t, reopened)
	ctx := offlineUpgradeContext(reopened, reopened.LastBlockHeight(), chainID)
	require.Equal(t, wantVersions, reopened.UpgradeKeeper.GetModuleVersionMap(ctx))
	require.Equal(t, []byte("[]"), ctx.KVStore(reopened.GetKey(paramstypes.StoreKey)).Get(v69OfflineDisabledMsgTypeURLsKey))
}
