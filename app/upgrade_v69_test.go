//go:build upgrade_v69

package app_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	sdkerrors "github.com/sei-protocol/sei-chain/sei-cosmos/types/errors"
	"github.com/sei-protocol/sei-chain/sei-cosmos/x/auth/signing"
	banktypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/bank/types"
	"github.com/sei-protocol/sei-chain/sei-cosmos/x/params"
	paramstypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/params/types"
	"github.com/sei-protocol/sei-chain/sei-cosmos/x/params/types/proposal"
	upgradetypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/upgrade/types"
	"github.com/sei-protocol/sei-chain/testutil/processblock"
	"github.com/sei-protocol/sei-chain/upgradetest"
	"github.com/stretchr/testify/require"
)

// v6.9 stores the disabled message type URLs, the params-module parameter that lists the
// messages a Cosmos transaction may not carry, as an empty list through the params 2 to 3
// migration. Governance refuses a change to a parameter that is not stored, so before v6.9 no
// proposal can disable a message type. These tests cover the stored parameter and the version
// map, a proposal that disables MsgSend once v6.9 has run, and the live two-binary path.

const (
	v69UpgradeName = "v6.9"
	// v69ParamsVersionBefore and v69ParamsVersion are the params consensus versions on either
	// side of the upgrade.
	v69ParamsVersionBefore uint64 = 2
	v69ParamsVersion       uint64 = 3

	v69KeyringPassword = "12345678\n"
)

// v69DisabledMsgTypeURLsKey is the params-store key of the disabled message type URLs.
var v69DisabledMsgTypeURLsKey = []byte(paramstypes.ModuleName + "/" + string(paramstypes.ParamStoreKeyDisabledMsgTypeURLs))

func newV69Chain(t *testing.T) *processblock.App {
	t.Helper()
	t.Setenv("UPGRADE_VERSION_LIST", v69UpgradeName)
	app := processblock.NewTestApp(t)
	processblock.CommonPreset(app)
	app.RegisterUpgradeHandlers()
	return app
}

func applyV69(t *testing.T, app *processblock.App) {
	t.Helper()
	app.UpgradeKeeper.ApplyUpgrade(app.Ctx(), upgradetypes.Plan{
		Name:   v69UpgradeName,
		Height: app.Ctx().BlockHeight(),
	})
}

// seedV68Params records params at its v6.8 consensus version and removes the disabled message
// type URLs from the params store, as every chain upgrading from v6.8 carries them.
func seedV68Params(t *testing.T, a *processblock.App) {
	t.Helper()
	versionMap := a.UpgradeKeeper.GetModuleVersionMap(a.Ctx())
	versionMap[paramstypes.ModuleName] = v69ParamsVersionBefore
	a.UpgradeKeeper.SetModuleVersionMap(a.Ctx(), versionMap)
	a.Ctx().KVStore(a.GetKey(paramstypes.StoreKey)).Delete(v69DisabledMsgTypeURLsKey)
}

// TestV69Upgrade requires v6.9 to store the disabled message type URLs as an empty list, to
// move params from consensus version 2 to 3, and to change nothing else in the params store or
// the module version map.
func TestV69Upgrade(t *testing.T) {
	a := newV69Chain(t)
	seedV68Params(t, a)
	store := a.Ctx().KVStore(a.GetKey(paramstypes.StoreKey))
	want := v69StoreEntries(t, store)
	want[string(v69DisabledMsgTypeURLsKey)] = []byte("[]")
	versions := a.UpgradeKeeper.GetModuleVersionMap(a.Ctx())
	versions[paramstypes.ModuleName] = v69ParamsVersion

	applyV69(t, a)

	require.Equal(t, want, v69StoreEntries(t, store),
		"v6.9 changed the params store beyond storing the disabled message type URLs")
	require.Equal(t, versions, a.UpgradeKeeper.GetModuleVersionMap(a.Ctx()),
		"v6.9 changed the module version map beyond moving params to %d", v69ParamsVersion)
	require.Empty(t, a.ParamsKeeper.GetDisabledMsgTypeURLs(a.Ctx()))
}

// TestV69LetsGovernanceDisableMessageTypes submits a parameter change proposal that disables
// MsgSend before and after v6.9, executes the one governance accepts, and delivers a bank send.
func TestV69LetsGovernanceDisableMessageTypes(t *testing.T) {
	a := newV69Chain(t)
	seedV68Params(t, a)
	sender := a.NewSignableAccount("v69/sender")
	a.FundAccount(sender, 100000000)
	msgSend := sdk.MsgTypeURL(&banktypes.MsgSend{})
	disableMsgSend := proposal.NewParameterChangeProposal("Disable MsgSend", "Bank sends move to EVM transfers",
		[]proposal.ParamChange{proposal.NewParamChange(
			paramstypes.ModuleName, string(paramstypes.ParamStoreKeyDisabledMsgTypeURLs), fmt.Sprintf("[%q]", msgSend),
		)}, false)
	_, err := a.GovKeeper.SubmitProposal(a.Ctx(), disableMsgSend)
	require.ErrorContains(t, err, "not found in subspace params")

	applyV69(t, a)
	_, err = a.GovKeeper.SubmitProposal(a.Ctx(), disableMsgSend)
	require.NoError(t, err)
	require.NoError(t, params.NewParamChangeProposalHandler(a.ParamsKeeper)(a.Ctx(), disableMsgSend))
	require.Equal(t, []string{msgSend}, a.ParamsKeeper.GetDisabledMsgTypeURLs(a.Ctx()))

	results := a.RunBlockDetailed([]signing.Tx{
		a.Sign(sender, 200000, banktypes.NewMsgSend(sender, a.NewAccount(), sdk.NewCoins(sdk.NewInt64Coin("usei", 1)))),
	})
	require.Len(t, results, 1)
	require.Equal(t, sdkerrors.ErrMsgTypeDisabled.ABCICode(), results[0].Code, results[0].Log)
	require.Equal(t, sdkerrors.RootCodespace, results[0].Codespace)
}

func TestV69CrossVersion(t *testing.T) {
	upgradetest.RunCrossVersion(t, seedV68State, verifyV69State)
}

// seedV68State records the v6.8 version map, with params at consensus version 2, and requires
// v6.8 not to store the disabled message type URLs.
func seedV68State(t *testing.T, chain *upgradetest.CrossVersion) {
	require.Equal(t, v69UpgradeName, chain.UpgradeName(t))
	versions := v69ModuleVersions(t, chain)
	require.Equal(t, v69ParamsVersionBefore, versions[paramstypes.ModuleName])
	require.Empty(t, chain.QueryStore(t, paramstypes.StoreKey, v69DisabledMsgTypeURLsKey),
		"v6.8 already stores the disabled message type URLs")
	chain.Record(t, "module_versions", versions)
}

// verifyV69State requires every node to agree on the blocks around the upgrade, the version map
// to move only params to 3, the disabled message type URLs to be stored as an empty list, and a
// Cosmos bank send to go through.
func verifyV69State(t *testing.T, chain *upgradetest.CrossVersion) {
	require.Equal(t, v69UpgradeName, chain.UpgradeName(t))

	appliedHeight := v69AppliedHeight(t, chain)
	require.Equal(t, chain.TargetHeight(t), appliedHeight)
	chain.Record(t, "applied_height", appliedHeight)
	// A header carries the application hash of the state its parent produced, so
	// the upgrade block's own result appears one height above the applied height.
	chain.RequireBlockAgreement(t, appliedHeight, appliedHeight+1, appliedHeight+2, appliedHeight+3)

	var before map[string]uint64
	chain.Replay(t, "module_versions", &before)
	want := maps.Clone(before)
	want[paramstypes.ModuleName] = v69ParamsVersion
	after := v69ModuleVersions(t, chain)
	chain.Record(t, "module_versions_after", after)
	require.Equal(t, want, after, "v6.9 changed the version map beyond moving params to %d", v69ParamsVersion)
	require.Equal(t, []byte("[]"), chain.QueryStore(t, paramstypes.StoreKey, v69DisabledMsgTypeURLsKey),
		"v6.9 did not store the disabled message type URLs as an empty list")

	chain.RequireDeliverTxSuccess(t, "v6.9 bank send", chain.Seid(v69KeyringPassword,
		"tx", "bank", "send", "admin", chain.KeyAddress(t, "sei-node-0", "node_admin"), "1usei",
		"--from", "admin",
		"--chain-id", "sei",
		"--fees", "200000usei",
		"--gas", "2000000",
		"--broadcast-mode", "sync",
		"--yes",
		"--output", "json",
	))
}

func v69StoreEntries(t *testing.T, store sdk.KVStore) map[string][]byte {
	t.Helper()
	iterator := store.Iterator(nil, nil)
	defer func() { require.NoError(t, iterator.Close()) }()
	entries := map[string][]byte{}
	for ; iterator.Valid(); iterator.Next() {
		entries[string(iterator.Key())] = append([]byte(nil), iterator.Value()...)
	}
	return entries
}

func v69ModuleVersions(t *testing.T, chain *upgradetest.CrossVersion) map[string]uint64 {
	t.Helper()
	output := chain.MustSeid(t, "", "q", "upgrade", "module_versions", "--output", "json")
	var response struct {
		ModuleVersions []struct {
			Name    string          `json:"name"`
			Version json.RawMessage `json:"version"`
		} `json:"module_versions"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &response), output)
	versions := make(map[string]uint64, len(response.ModuleVersions))
	for _, entry := range response.ModuleVersions {
		version, err := strconv.ParseUint(strings.Trim(string(entry.Version), `"`), 10, 64)
		require.NoError(t, err, "module %s has version %s", entry.Name, entry.Version)
		versions[entry.Name] = version
	}
	return versions
}

func v69AppliedHeight(t *testing.T, chain *upgradetest.CrossVersion) int64 {
	t.Helper()
	output := chain.MustSeid(t, "", "q", "upgrade", "applied", v69UpgradeName, "--output", "json")
	var applied struct {
		Header struct {
			Height json.RawMessage `json:"height"`
		} `json:"header"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &applied), output)
	height, err := strconv.ParseInt(strings.Trim(string(applied.Header.Height), `"`), 10, 64)
	require.NoError(t, err, "applied height %s", applied.Header.Height)
	return height
}
