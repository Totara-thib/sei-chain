//go:build upgrade_v69 && offline_upgrade && upgrade_source

package app

import (
	"path/filepath"
	"testing"
	"time"

	paramstypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/params/types"
	upgradetypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/upgrade/types"
	"github.com/stretchr/testify/require"
)

const v69OfflineUpgradeName = "v6.9"

var (
	// v69OfflineDisabledMsgTypeURLsKey is the params-store key of the parameter v6.9 stores,
	// which v6.8 does not register.
	v69OfflineDisabledMsgTypeURLsKey = []byte(paramstypes.ModuleName + "/DisabledMsgTypeURLs")
	v69OfflineSourceStores           = []string{paramstypes.StoreKey, upgradetypes.StoreKey}
)

func TestV69OfflineUpgradeSource(t *testing.T) {
	root := requireOfflineUpgradePhase(t, "source")
	testApp := openOfflineUpgradeApp(t, root, true)
	ctx := testApp.GetContextForDeliverTx(nil).WithBlockTime(time.Now().UTC())
	require.Nil(t, ctx.KVStore(testApp.GetKey(paramstypes.StoreKey)).Get(v69OfflineDisabledMsgTypeURLsKey),
		"v6.8 already stores the disabled message type URLs")

	upgradeHeight := ctx.BlockHeight() + 2
	require.NoError(t, testApp.UpgradeKeeper.ScheduleUpgrade(ctx, upgradetypes.Plan{
		Name: v69OfflineUpgradeName, Height: upgradeHeight,
	}))
	commitOfflineUpgradeApp(t, testApp)
	sourceHeight := testApp.LastBlockHeight()
	plan, found := committedOfflineUpgradePlan(t, testApp)
	require.True(t, found, "scheduled upgrade plan was not committed")
	require.Equal(t, v69OfflineUpgradeName, plan.Name)
	require.Equal(t, upgradeHeight, plan.Height)
	moduleVersions := offlineUpgradeModuleVersions(t, testApp)
	stores := snapshotOfflineUpgradeStores(t, testApp, ctx, v69OfflineSourceStores)
	closeOfflineUpgradeApp(t, testApp)
	writeOfflineUpgradeArtifact(t, root, offlineUpgradeArtifact{
		Upgrade: plan.Name, SourceHeight: sourceHeight, UpgradeHeight: upgradeHeight,
		ModuleVersions: moduleVersions, Stores: stores,
	})
}

// TestV69OfflineUpgradeReopen requires v6.8 to reopen the database v6.9 migrated, since v6.9
// deletes no store, and to read the params-module parameters it registers.
func TestV69OfflineUpgradeReopen(t *testing.T) {
	root := requireOfflineUpgradePhase(t, "reopen")
	artifact := readOfflineUpgradeArtifact(t, root)
	require.Equal(t, v69OfflineUpgradeName, artifact.Upgrade)
	require.NotEmpty(t, artifact.UpgradeHash)
	migrated := offlineUpgradeMigratedDatabase(t, root, artifact)
	reopenRoot := filepath.Join(root, "reopen")
	copyOfflineUpgradeDatabase(t, migrated, reopenRoot)

	testApp := openOfflineUpgradeApp(t, reopenRoot, false)
	defer closeOfflineUpgradeApp(t, testApp)
	require.Equal(t, artifact.UpgradeHeight, testApp.LastBlockHeight())
	require.Equal(t, artifact.UpgradeHash, offlineUpgradeHashString(committedOfflineUpgradeHash(t, testApp)))
	ctx := offlineUpgradeReadContext(testApp, testApp.LastBlockHeight())
	require.Equal(t, []byte("[]"), ctx.KVStore(testApp.GetKey(paramstypes.StoreKey)).Get(v69OfflineDisabledMsgTypeURLsKey))
	require.NotZero(t, testApp.ParamsKeeper.GetCosmosGasParams(ctx).CosmosGasMultiplierDenominator)
	require.NotEmpty(t, testApp.ParamsKeeper.GetFeesParams(ctx).GlobalMinimumGasPrices)
}
