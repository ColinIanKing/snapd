// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2015-2016 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/jessevdk/go-flags"

	"github.com/snapcore/snapd/asserts"
	"github.com/snapcore/snapd/asserts/snapasserts"
	"github.com/snapcore/snapd/client"
	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/i18n"
	"github.com/snapcore/snapd/interfaces"
	"github.com/snapcore/snapd/logger"
	"github.com/snapcore/snapd/osutil"
	"github.com/snapcore/snapd/overlord/assertstate"
	"github.com/snapcore/snapd/overlord/auth"
	"github.com/snapcore/snapd/overlord/configstate"
	"github.com/snapcore/snapd/overlord/devicestate"
	"github.com/snapcore/snapd/overlord/hookstate/ctlcmd"
	"github.com/snapcore/snapd/overlord/ifacestate"
	"github.com/snapcore/snapd/overlord/snapstate"
	"github.com/snapcore/snapd/overlord/state"
	"github.com/snapcore/snapd/progress"
	"github.com/snapcore/snapd/release"
	"github.com/snapcore/snapd/snap"
	"github.com/snapcore/snapd/store"
)

var api = []*Command{
	rootCmd,
	sysInfoCmd,
	loginCmd,
	logoutCmd,
	appIconCmd,
	findCmd,
	snapsCmd,
	snapCmd,
	snapConfCmd,
	interfacesCmd,
	assertsCmd,
	assertsFindManyCmd,
	eventsCmd,
	stateChangeCmd,
	stateChangesCmd,
	createUserCmd,
	buyCmd,
	readyToBuyCmd,
	paymentMethodsCmd,
	snapctlCmd,
}

var (
	rootCmd = &Command{
		Path:    "/",
		GuestOK: true,
		GET:     tbd,
	}

	sysInfoCmd = &Command{
		Path:    "/v2/system-info",
		GuestOK: true,
		GET:     sysInfo,
	}

	loginCmd = &Command{
		Path: "/v2/login",
		POST: loginUser,
	}

	logoutCmd = &Command{
		Path:   "/v2/logout",
		POST:   logoutUser,
		UserOK: true,
	}

	appIconCmd = &Command{
		Path:   "/v2/icons/{name}/icon",
		UserOK: true,
		GET:    appIconGet,
	}

	findCmd = &Command{
		Path:   "/v2/find",
		UserOK: true,
		GET:    searchStore,
	}

	snapsCmd = &Command{
		Path:   "/v2/snaps",
		UserOK: true,
		GET:    getSnapsInfo,
		POST:   postSnaps,
	}

	snapCmd = &Command{
		Path:   "/v2/snaps/{name}",
		UserOK: true,
		GET:    getSnapInfo,
		POST:   postSnap,
	}

	snapConfCmd = &Command{
		Path: "/v2/snaps/{name}/conf",
		GET:  getSnapConf,
		PUT:  setSnapConf,
	}

	interfacesCmd = &Command{
		Path:   "/v2/interfaces",
		UserOK: true,
		GET:    getInterfaces,
		POST:   changeInterfaces,
	}

	// TODO: allow to post assertions for UserOK? they are verified anyway
	assertsCmd = &Command{
		Path: "/v2/assertions",
		POST: doAssert,
	}

	assertsFindManyCmd = &Command{
		Path:   "/v2/assertions/{assertType}",
		UserOK: true,
		GET:    assertsFindMany,
	}

	eventsCmd = &Command{
		Path: "/v2/events",
		GET:  getEvents,
	}

	stateChangeCmd = &Command{
		Path:   "/v2/changes/{id}",
		UserOK: true,
		GET:    getChange,
		POST:   abortChange,
	}

	stateChangesCmd = &Command{
		Path:   "/v2/changes",
		UserOK: true,
		GET:    getChanges,
	}

	createUserCmd = &Command{
		Path:   "/v2/create-user",
		UserOK: false,
		POST:   postCreateUser,
	}

	buyCmd = &Command{
		Path:   "/v2/buy",
		UserOK: false,
		POST:   postBuy,
	}

	readyToBuyCmd = &Command{
		Path:   "/v2/buy/ready",
		UserOK: false,
		GET:    readyToBuy,
	}

	// TODO Remove once the CLI is using the new /buy/ready endpoint
	paymentMethodsCmd = &Command{
		Path:   "/v2/buy/methods",
		UserOK: false,
		GET:    getPaymentMethods,
	}

	snapctlCmd = &Command{
		Path:   "/v2/snapctl",
		SnapOK: true,
		POST:   runSnapctl,
	}
)

func tbd(c *Command, r *http.Request, user *auth.UserState) Response {
	return SyncResponse([]string{"TBD"}, nil)
}

func sysInfo(c *Command, r *http.Request, user *auth.UserState) Response {
	m := map[string]interface{}{
		"series":     release.Series,
		"version":    c.d.Version,
		"os-release": release.ReleaseInfo,
		"on-classic": release.OnClassic,
	}

	// TODO: set the store-id here from the model information
	if storeID := os.Getenv("UBUNTU_STORE_ID"); storeID != "" {
		m["store"] = storeID
	}

	return SyncResponse(m, nil)
}

type loginResponseData struct {
	Macaroon   string   `json:"macaroon,omitempty"`
	Discharges []string `json:"discharges,omitempty"`
}

var isEmailish = regexp.MustCompile(`.@.*\..`).MatchString

func loginUser(c *Command, r *http.Request, user *auth.UserState) Response {
	var loginData struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Otp      string `json:"otp"`
	}

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&loginData); err != nil {
		return BadRequest("cannot decode login data from request body: %v", err)
	}

	// the "username" needs to look a lot like an email address
	if !isEmailish(loginData.Username) {
		return SyncResponse(&resp{
			Type: ResponseTypeError,
			Result: &errorResult{
				Message: "please use a valid email address.",
				Kind:    errorKindInvalidAuthData,
				Value:   map[string][]string{"email": {"invalid"}},
			},
			Status: http.StatusBadRequest,
		}, nil)
	}

	macaroon, discharge, err := store.LoginUser(loginData.Username, loginData.Password, loginData.Otp)
	switch err {
	case store.ErrAuthenticationNeeds2fa:
		return SyncResponse(&resp{
			Type: ResponseTypeError,
			Result: &errorResult{
				Kind:    errorKindTwoFactorRequired,
				Message: err.Error(),
			},
			Status: http.StatusUnauthorized,
		}, nil)
	case store.Err2faFailed:
		return SyncResponse(&resp{
			Type: ResponseTypeError,
			Result: &errorResult{
				Kind:    errorKindTwoFactorFailed,
				Message: err.Error(),
			},
			Status: http.StatusUnauthorized,
		}, nil)
	default:
		if err, ok := err.(store.ErrInvalidAuthData); ok {
			return SyncResponse(&resp{
				Type: ResponseTypeError,
				Result: &errorResult{
					Message: err.Error(),
					Kind:    errorKindInvalidAuthData,
					Value:   err,
				},
				Status: http.StatusBadRequest,
			}, nil)
		}
		return Unauthorized(err.Error())
	case nil:
		// continue
	}

	overlord := c.d.overlord
	state := overlord.State()
	state.Lock()
	_, err = auth.NewUser(state, loginData.Username, macaroon, []string{discharge})
	state.Unlock()
	if err != nil {
		return InternalError("cannot persist authentication details: %v", err)
	}

	result := loginResponseData{
		Macaroon:   macaroon,
		Discharges: []string{discharge},
	}
	return SyncResponse(result, nil)
}

func logoutUser(c *Command, r *http.Request, user *auth.UserState) Response {
	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	if user == nil {
		return BadRequest("not logged in")
	}
	err := auth.RemoveUser(state, user.ID)
	if err != nil {
		return InternalError(err.Error())
	}

	return SyncResponse(nil, nil)
}

// UserFromRequest extracts user information from request and return the respective user in state, if valid
// It requires the state to be locked
func UserFromRequest(st *state.State, req *http.Request) (*auth.UserState, error) {
	// extract macaroons data from request
	header := req.Header.Get("Authorization")
	if header == "" {
		return nil, auth.ErrInvalidAuth
	}

	authorizationData := strings.SplitN(header, " ", 2)
	if len(authorizationData) != 2 || authorizationData[0] != "Macaroon" {
		return nil, fmt.Errorf("authorization header misses Macaroon prefix")
	}

	var macaroon string
	var discharges []string
	for _, field := range strings.Split(authorizationData[1], ",") {
		field := strings.TrimSpace(field)
		if strings.HasPrefix(field, `root="`) {
			macaroon = strings.TrimSuffix(field[6:], `"`)
		}
		if strings.HasPrefix(field, `discharge="`) {
			discharges = append(discharges, strings.TrimSuffix(field[11:], `"`))
		}
	}

	if macaroon == "" || len(discharges) == 0 {
		return nil, fmt.Errorf("invalid authorization header")
	}

	user, err := auth.CheckMacaroon(st, macaroon, discharges)
	return user, err
}

var muxVars = mux.Vars

func getSnapInfo(c *Command, r *http.Request, user *auth.UserState) Response {
	vars := muxVars(r)
	name := vars["name"]

	localSnap, active, err := localSnapInfo(c.d.overlord.State(), name)
	if err != nil {
		if err == errNoSnap {
			return NotFound("cannot find snap %q", name)
		}

		return InternalError("%v", err)
	}

	route := c.d.router.Get(c.Path)
	if route == nil {
		return InternalError("cannot find route for snap %s", name)
	}

	url, err := route.URL("name", name)
	if err != nil {
		return InternalError("cannot build URL for snap %s: %v", name, err)
	}

	result := webify(mapLocal(localSnap, active), url.String())

	return SyncResponse(result, nil)
}

func webify(result map[string]interface{}, resource string) map[string]interface{} {
	result["resource"] = resource

	icon, ok := result["icon"].(string)
	if !ok || icon == "" || strings.HasPrefix(icon, "http") {
		return result
	}
	result["icon"] = ""

	route := appIconCmd.d.router.Get(appIconCmd.Path)
	if route != nil {
		name, _ := result["name"].(string)
		url, err := route.URL("name", name)
		if err == nil {
			result["icon"] = url.String()
		}
	}

	return result
}

func getStore(c *Command) snapstate.StoreService {
	st := c.d.overlord.State()
	st.Lock()
	defer st.Unlock()

	return snapstate.Store(st)
}

func searchStore(c *Command, r *http.Request, user *auth.UserState) Response {
	route := c.d.router.Get(snapCmd.Path)
	if route == nil {
		return InternalError("cannot find route for snaps")
	}
	query := r.URL.Query()
	q := query.Get("q")
	name := query.Get("name")
	private := false
	prefix := false

	if name != "" {
		if q != "" {
			return BadRequest("cannot use 'q' and 'name' together")
		}

		if name[len(name)-1] != '*' {
			return findOne(c, r, user, name)
		}

		prefix = true
		q = name[:len(name)-1]
	}

	if sel := query.Get("select"); sel != "" {
		switch sel {
		case "refresh":
			if prefix {
				return BadRequest("cannot use 'name' with 'select=refresh'")
			}
			if q != "" {
				return BadRequest("cannot use 'q' with 'select=refresh'")
			}
			return storeUpdates(c, r, user)
		case "private":
			private = true
		}
	}

	theStore := getStore(c)
	found, err := theStore.Find(&store.Search{
		Query:   q,
		Private: private,
		Prefix:  prefix,
	}, user)
	switch err {
	case nil:
		// pass
	case store.ErrEmptyQuery, store.ErrBadQuery:
		return BadRequest("%v", err)
	case store.ErrUnauthenticated:
		return Unauthorized(err.Error())
	default:
		return InternalError("%v", err)
	}

	meta := &Meta{
		SuggestedCurrency: theStore.SuggestedCurrency(),
		Sources:           []string{"store"},
	}

	return sendStorePackages(route, meta, found)
}

func findOne(c *Command, r *http.Request, user *auth.UserState, name string) Response {
	if err := snap.ValidateName(name); err != nil {
		return BadRequest(err.Error())
	}

	theStore := getStore(c)
	snapInfo, err := theStore.Snap(name, "", false, snap.R(0), user)
	if err != nil {
		return InternalError("%v", err)
	}

	meta := &Meta{
		SuggestedCurrency: theStore.SuggestedCurrency(),
		Sources:           []string{"store"},
	}

	results := make([]*json.RawMessage, 1)
	data, err := json.Marshal(webify(mapRemote(snapInfo), r.URL.String()))
	if err != nil {
		return InternalError(err.Error())
	}
	results[0] = (*json.RawMessage)(&data)
	return SyncResponse(results, meta)
}

func shouldSearchStore(r *http.Request) bool {
	// we should jump to the old behaviour iff q is given, or if
	// sources is given and either empty or contains the word
	// 'store'.  Otherwise, local results only.

	query := r.URL.Query()

	if _, ok := query["q"]; ok {
		logger.Debugf("use of obsolete \"q\" parameter: %q", r.URL)
		return true
	}

	if src, ok := query["sources"]; ok {
		logger.Debugf("use of obsolete \"sources\" parameter: %q", r.URL)
		if len(src) == 0 || strings.Contains(src[0], "store") {
			return true
		}
	}

	return false
}

func storeUpdates(c *Command, r *http.Request, user *auth.UserState) Response {
	route := c.d.router.Get(snapCmd.Path)
	if route == nil {
		return InternalError("cannot find route for snaps")
	}

	state := c.d.overlord.State()
	state.Lock()
	updates, err := snapstateRefreshCandidates(state, user)
	state.Unlock()
	if err != nil {
		return InternalError("cannot list updates: %v", err)
	}

	return sendStorePackages(route, nil, updates)
}

func sendStorePackages(route *mux.Route, meta *Meta, found []*snap.Info) Response {
	results := make([]*json.RawMessage, 0, len(found))
	for _, x := range found {
		url, err := route.URL("name", x.Name())
		if err != nil {
			logger.Noticef("Cannot build URL for snap %q revision %s: %v", x.Name(), x.Revision, err)
			continue
		}

		data, err := json.Marshal(webify(mapRemote(x), url.String()))
		if err != nil {
			return InternalError("%v", err)
		}
		raw := json.RawMessage(data)
		results = append(results, &raw)
	}

	return SyncResponse(results, meta)
}

// plural!
func getSnapsInfo(c *Command, r *http.Request, user *auth.UserState) Response {

	if shouldSearchStore(r) {
		logger.Noticef("Jumping to \"find\" to better support legacy request %q", r.URL)
		return searchStore(c, r, user)
	}

	route := c.d.router.Get(snapCmd.Path)
	if route == nil {
		return InternalError("cannot find route for snaps")
	}

	found, err := allLocalSnapInfos(c.d.overlord.State())
	if err != nil {
		return InternalError("cannot list local snaps! %v", err)
	}

	results := make([]*json.RawMessage, len(found))

	for i, x := range found {
		name := x.info.Name()
		rev := x.info.Revision

		url, err := route.URL("name", name)
		if err != nil {
			logger.Noticef("Cannot build URL for snap %q revision %s: %v", name, rev, err)
			continue
		}

		data, err := json.Marshal(webify(mapLocal(x.info, x.snapst), url.String()))
		if err != nil {
			return InternalError("cannot serialize snap %q revision %s: %v", name, rev, err)
		}
		raw := json.RawMessage(data)
		results[i] = &raw
	}

	return SyncResponse(results, &Meta{Sources: []string{"local"}})
}

func resultHasType(r map[string]interface{}, allowedTypes []string) bool {
	for _, t := range allowedTypes {
		if r["type"] == t {
			return true
		}
	}
	return false
}

// licenseData holds details about the snap license, and may be
// marshaled back as an error when the license agreement is pending,
// and is expected as input to accept (or not) that license
// agreement. As such, its field names are part of the API.
type licenseData struct {
	Intro   string `json:"intro"`
	License string `json:"license"`
	Agreed  bool   `json:"agreed"`
}

func (*licenseData) Error() string {
	return "license agreement required"
}

type snapInstruction struct {
	progress.NullProgress
	Action   string        `json:"action"`
	Channel  string        `json:"channel"`
	Revision snap.Revision `json:"revision"`
	DevMode  bool          `json:"devmode"`
	JailMode bool          `json:"jailmode"`
	// dropping support temporarely until flag confusion is sorted,
	// this isn't supported by client atm anyway
	LeaveOld bool         `json:"temp-dropped-leave-old"`
	License  *licenseData `json:"license"`
	Snaps    []string     `json:"snaps"`

	// The fields below should not be unmarshalled into. Do not export them.
	userID int
}

var (
	snapstateGet               = snapstate.Get
	snapstateInstall           = snapstate.Install
	snapstateInstallPath       = snapstate.InstallPath
	snapstateRefreshCandidates = snapstate.RefreshCandidates
	snapstateTryPath           = snapstate.TryPath
	snapstateUpdate            = snapstate.Update
	snapstateUpdateMany        = snapstate.UpdateMany
	snapstateInstallMany       = snapstate.InstallMany
	snapstateRemoveMany        = snapstate.RemoveMany

	assertstateRefreshSnapDeclarations = assertstate.RefreshSnapDeclarations
)

func ensureStateSoonImpl(st *state.State) {
	st.EnsureBefore(0)
}

var ensureStateSoon = ensureStateSoonImpl

var errNothingToInstall = errors.New("nothing to install")

func ensureUbuntuCore(st *state.State, targetSnap string, userID int) (*state.TaskSet, error) {
	ubuntuCore := "ubuntu-core"

	if targetSnap == ubuntuCore {
		return nil, errNothingToInstall
	}

	var ss snapstate.SnapState

	err := snapstateGet(st, ubuntuCore, &ss)
	if err != state.ErrNoState {
		return nil, err
	}

	return snapstateInstall(st, ubuntuCore, "stable", snap.R(0), userID, 0)
}

func withEnsureUbuntuCore(st *state.State, targetSnap string, userID int, install func() (*state.TaskSet, error)) ([]*state.TaskSet, error) {
	ubuCoreTs, err := ensureUbuntuCore(st, targetSnap, userID)
	if err != nil && err != errNothingToInstall {
		return nil, err
	}

	ts, err := install()
	if err != nil {
		return nil, err
	}

	// ensure main install waits on ubuntu core install
	if ubuCoreTs != nil {
		ts.WaitAll(ubuCoreTs)
		return []*state.TaskSet{ubuCoreTs, ts}, nil
	}

	return []*state.TaskSet{ts}, nil
}

var errModeConflict = errors.New("cannot use devmode and jailmode flags together")
var errNoJailMode = errors.New("this system cannot honour the jailmode flag")

func modeFlags(devMode, jailMode bool) (snapstate.Flags, error) {
	devModeOS := release.ReleaseInfo.ForceDevMode()
	flags := snapstate.Flags(0)
	if jailMode {
		if devModeOS {
			return 0, errNoJailMode
		}
		if devMode {
			return 0, errModeConflict
		}
		flags |= snapstate.JailMode
	}
	if devMode || devModeOS {
		flags |= snapstate.DevMode
	}

	return flags, nil

}

func snapUpdateMany(inst *snapInstruction, st *state.State) (msg string, updated []string, tasksets []*state.TaskSet, err error) {
	// we need refreshed snap-declarations to enforce refresh-control as best as we can, this also ensures that snap-declarations and their prerequisite assertions are updated regularly
	if err := assertstateRefreshSnapDeclarations(st, inst.userID); err != nil {
		return "", nil, nil, err
	}

	updated, tasksets, err = snapstateUpdateMany(st, inst.Snaps, inst.userID)
	if err != nil {
		return "", nil, nil, err
	}

	switch len(inst.Snaps) {
	case 0:
		// all snaps
		msg = i18n.G("Refresh all snaps in the system")
	case 1:
		msg = fmt.Sprintf(i18n.G("Refresh snap %q"), inst.Snaps[0])
	default:
		quoted := make([]string, len(inst.Snaps))
		for i, name := range inst.Snaps {
			quoted[i] = strconv.Quote(name)
		}
		// TRANSLATORS: the %s is a comma-separated list of quoted snap names
		msg = fmt.Sprintf(i18n.G("Refresh snaps %s"), strings.Join(quoted, ", "))
	}

	return msg, updated, tasksets, nil
}

func snapInstallMany(inst *snapInstruction, st *state.State) (msg string, installed []string, tasksets []*state.TaskSet, err error) {
	installed, tasksets, err = snapstateInstallMany(st, inst.Snaps, inst.userID)
	if err != nil {
		return "", nil, nil, err
	}

	switch len(inst.Snaps) {
	case 0:
		return "", nil, nil, fmt.Errorf("cannot install zero snaps")
	case 1:
		msg = fmt.Sprintf(i18n.G("Install snap %q"), inst.Snaps[0])
	default:
		quoted := make([]string, len(inst.Snaps))
		for i, name := range inst.Snaps {
			quoted[i] = strconv.Quote(name)
		}
		// TRANSLATORS: the %s is a comma-separated list of quoted snap names
		msg = fmt.Sprintf(i18n.G("Install snaps %s"), strings.Join(quoted, ", "))
	}

	return msg, installed, tasksets, nil
}

func snapInstall(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	flags, err := modeFlags(inst.DevMode, inst.JailMode)
	if err != nil {
		return "", nil, err
	}

	logger.Noticef("Installing snap %q revision %s", inst.Snaps[0], inst.Revision)

	tsets, err := withEnsureUbuntuCore(st, inst.Snaps[0], inst.userID,
		func() (*state.TaskSet, error) {
			return snapstateInstall(st, inst.Snaps[0], inst.Channel, inst.Revision, inst.userID, flags)
		},
	)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Install %q snap"), inst.Snaps[0])
	if inst.Channel != "stable" && inst.Channel != "" {
		msg = fmt.Sprintf(i18n.G("Install %q snap from %q channel"), inst.Snaps[0], inst.Channel)
	}
	return msg, tsets, nil
}

func snapUpdate(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	// TODO: bail if revision is given (and != current?), *or* behave as with install --revision?
	flags, err := modeFlags(inst.DevMode, inst.JailMode)
	if err != nil {
		return "", nil, err
	}

	// we need refreshed snap-declarations to enforce refresh-control as best as we can
	if err = assertstateRefreshSnapDeclarations(st, inst.userID); err != nil {
		return "", nil, err
	}

	ts, err := snapstateUpdate(st, inst.Snaps[0], inst.Channel, inst.Revision, inst.userID, flags)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Refresh %q snap"), inst.Snaps[0])
	if inst.Channel != "stable" && inst.Channel != "" {
		msg = fmt.Sprintf(i18n.G("Refresh %q snap from %q channel"), inst.Snaps[0], inst.Channel)
	}

	return msg, []*state.TaskSet{ts}, nil
}

func snapRemoveMany(inst *snapInstruction, st *state.State) (msg string, removed []string, tasksets []*state.TaskSet, err error) {
	removed, tasksets, err = snapstateRemoveMany(st, inst.Snaps)
	if err != nil {
		return "", nil, nil, err
	}

	switch len(inst.Snaps) {
	case 0:
		return "", nil, nil, fmt.Errorf("cannot remove zero snaps")
	case 1:
		msg = fmt.Sprintf(i18n.G("Remove snap %q"), inst.Snaps[0])
	default:
		quoted := make([]string, len(inst.Snaps))
		for i, name := range inst.Snaps {
			quoted[i] = strconv.Quote(name)
		}
		// TRANSLATORS: the %s is a comma-separated list of quoted snap names
		msg = fmt.Sprintf(i18n.G("Remove snaps %s"), strings.Join(quoted, ", "))
	}

	return msg, removed, tasksets, nil
}

func snapRemove(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	ts, err := snapstate.Remove(st, inst.Snaps[0], inst.Revision)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Remove %q snap"), inst.Snaps[0])
	return msg, []*state.TaskSet{ts}, nil
}

func snapRevert(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	var ts *state.TaskSet

	flags, err := modeFlags(inst.DevMode, inst.JailMode)
	if err != nil {
		return "", nil, err
	}

	if inst.Revision.Unset() {
		ts, err = snapstate.Revert(st, inst.Snaps[0], flags)
	} else {
		ts, err = snapstate.RevertToRevision(st, inst.Snaps[0], inst.Revision, flags)
	}
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Revert %q snap"), inst.Snaps[0])
	return msg, []*state.TaskSet{ts}, nil
}

func snapEnable(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	if !inst.Revision.Unset() {
		return "", nil, errors.New("enable takes no revision")
	}
	ts, err := snapstate.Enable(st, inst.Snaps[0])
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Enable %q snap"), inst.Snaps[0])
	return msg, []*state.TaskSet{ts}, nil
}

func snapDisable(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	if !inst.Revision.Unset() {
		return "", nil, errors.New("disable takes no revision")
	}
	ts, err := snapstate.Disable(st, inst.Snaps[0])
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Disable %q snap"), inst.Snaps[0])
	return msg, []*state.TaskSet{ts}, nil
}

type snapActionFunc func(*snapInstruction, *state.State) (string, []*state.TaskSet, error)

var snapInstructionDispTable = map[string]snapActionFunc{
	"install": snapInstall,
	"refresh": snapUpdate,
	"remove":  snapRemove,
	"revert":  snapRevert,
	"enable":  snapEnable,
	"disable": snapDisable,
}

func (inst *snapInstruction) dispatch() snapActionFunc {
	if len(inst.Snaps) != 1 {
		logger.Panicf("dispatch only handles single-snap ops; got %d", len(inst.Snaps))
	}
	return snapInstructionDispTable[inst.Action]
}

func postSnap(c *Command, r *http.Request, user *auth.UserState) Response {
	route := c.d.router.Get(stateChangeCmd.Path)
	if route == nil {
		return InternalError("cannot find route for change")
	}

	decoder := json.NewDecoder(r.Body)
	var inst snapInstruction
	if err := decoder.Decode(&inst); err != nil {
		return BadRequest("cannot decode request body into snap instruction: %v", err)
	}

	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	if user != nil {
		inst.userID = user.ID
	}

	vars := muxVars(r)
	inst.Snaps = []string{vars["name"]}

	impl := inst.dispatch()
	if impl == nil {
		return BadRequest("unknown action %s", inst.Action)
	}

	msg, tsets, err := impl(&inst, state)
	if err != nil {
		return BadRequest("cannot %s %q: %v", inst.Action, inst.Snaps[0], err)
	}

	chg := newChange(state, inst.Action+"-snap", msg, tsets, inst.Snaps)

	ensureStateSoon(state)

	return AsyncResponse(nil, &Meta{Change: chg.ID()})
}

func newChange(st *state.State, kind, summary string, tsets []*state.TaskSet, snapNames []string) *state.Change {
	chg := st.NewChange(kind, summary)
	for _, ts := range tsets {
		chg.AddAll(ts)
	}
	if snapNames != nil {
		chg.Set("snap-names", snapNames)
	}
	return chg
}

const maxReadBuflen = 1024 * 1024

func trySnap(c *Command, r *http.Request, user *auth.UserState, trydir string, flags snapstate.Flags) Response {
	st := c.d.overlord.State()
	st.Lock()
	defer st.Unlock()

	if !filepath.IsAbs(trydir) {
		return BadRequest("cannot try %q: need an absolute path", trydir)
	}
	if !osutil.IsDirectory(trydir) {
		return BadRequest("cannot try %q: not a snap directory", trydir)
	}

	// the developer asked us to do this with a trusted snap dir
	info, err := unsafeReadSnapInfo(trydir)
	if err != nil {
		return BadRequest("cannot read snap info for %s: %s", trydir, err)
	}

	tsets, err := snapstateTryPath(st, info.Name(), trydir, flags)
	if err != nil {
		return BadRequest("cannot try %s: %s", trydir, err)
	}

	msg := fmt.Sprintf(i18n.G("Try %q snap from %q"), info.Name(), trydir)
	chg := newChange(st, "try-snap", msg, []*state.TaskSet{tsets}, []string{info.Name()})
	chg.Set("api-data", map[string]string{"snap-name": info.Name()})

	ensureStateSoon(st)

	return AsyncResponse(nil, &Meta{Change: chg.ID()})
}

func isTrue(form *multipart.Form, key string) bool {
	value := form.Value[key]
	if len(value) == 0 {
		return false
	}
	b, err := strconv.ParseBool(value[0])
	if err != nil {
		return false
	}

	return b
}

func snapsOp(c *Command, r *http.Request, user *auth.UserState) Response {
	route := c.d.router.Get(stateChangeCmd.Path)
	if route == nil {
		return InternalError("cannot find route for change")
	}

	decoder := json.NewDecoder(r.Body)
	var inst snapInstruction
	if err := decoder.Decode(&inst); err != nil {
		return BadRequest("cannot decode request body into snap instruction: %v", err)
	}

	if inst.Channel != "" || !inst.Revision.Unset() || inst.DevMode || inst.JailMode {
		return BadRequest("unsupported option provided for multi-snap operation")
	}

	st := c.d.overlord.State()
	st.Lock()
	defer st.Unlock()

	if user != nil {
		inst.userID = user.ID
	}

	var msg string
	var affected []string
	var tsets []*state.TaskSet
	var err error
	switch inst.Action {
	case "refresh":
		msg, affected, tsets, err = snapUpdateMany(&inst, st)
	case "install":
		msg, affected, tsets, err = snapInstallMany(&inst, st)
	case "remove":
		msg, affected, tsets, err = snapRemoveMany(&inst, st)
	default:
		return BadRequest("unsupported multi-snap operation %q", inst.Action)
	}
	if err != nil {
		return InternalError("cannot %s %q: %v", inst.Action, inst.Snaps, err)
	}

	var chg *state.Change
	if len(tsets) == 0 {
		chg = st.NewChange(inst.Action+"-snap", msg)
		chg.SetStatus(state.DoneStatus)
	} else {
		chg = newChange(st, inst.Action+"-snap", msg, tsets, affected)
		ensureStateSoon(st)
	}
	chg.Set("api-data", map[string]interface{}{"snap-names": affected})

	return AsyncResponse(nil, &Meta{Change: chg.ID()})
}

func postSnaps(c *Command, r *http.Request, user *auth.UserState) Response {
	contentType := r.Header.Get("Content-Type")

	if contentType == "application/json" {
		return snapsOp(c, r, user)
	}

	if !strings.HasPrefix(contentType, "multipart/") {
		return BadRequest("unknown content type: %s", contentType)
	}

	route := c.d.router.Get(stateChangeCmd.Path)
	if route == nil {
		return InternalError("cannot find route for change")
	}

	// POSTs to sideload snaps must be a multipart/form-data file upload.
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return BadRequest("cannot parse POST body: %v", err)
	}

	form, err := multipart.NewReader(r.Body, params["boundary"]).ReadForm(maxReadBuflen)
	if err != nil {
		return BadRequest("cannot read POST form: %v", err)
	}

	dangerousOK := isTrue(form, "dangerous")
	devmode := isTrue(form, "devmode")
	flags, err := modeFlags(devmode, isTrue(form, "jailmode"))
	if err != nil {
		return BadRequest(err.Error())
	}

	if len(form.Value["action"]) > 0 && form.Value["action"][0] == "try" {
		if len(form.Value["snap-path"]) == 0 {
			return BadRequest("need 'snap-path' value in form")
		}
		return trySnap(c, r, user, form.Value["snap-path"][0], flags)
	}

	// find the file for the "snap" form field
	var snapBody multipart.File
	var origPath string
out:
	for name, fheaders := range form.File {
		if name != "snap" {
			continue
		}
		for _, fheader := range fheaders {
			snapBody, err = fheader.Open()
			origPath = fheader.Filename
			if err != nil {
				return BadRequest(`cannot open uploaded "snap" file: %v`, err)
			}
			defer snapBody.Close()

			break out
		}
	}
	defer form.RemoveAll()

	if snapBody == nil {
		return BadRequest(`cannot find "snap" file field in provided multipart/form-data payload`)
	}

	tmpf, err := ioutil.TempFile("", "snapd-sideload-pkg-")
	if err != nil {
		return InternalError("cannot create temporary file: %v", err)
	}

	if _, err := io.Copy(tmpf, snapBody); err != nil {
		os.Remove(tmpf.Name())
		return InternalError("cannot copy request into temporary file: %v", err)
	}
	tmpf.Sync()

	tempPath := tmpf.Name()

	if len(form.Value["snap-path"]) > 0 {
		origPath = form.Value["snap-path"][0]
	}

	st := c.d.overlord.State()
	st.Lock()
	defer st.Unlock()

	var snapName string
	var sideInfo *snap.SideInfo

	if !dangerousOK {
		si, err := snapasserts.DeriveSideInfo(tempPath, assertstate.DB(st))
		switch err {
		case nil:
			snapName = si.RealName
			sideInfo = si
		case asserts.ErrNotFound:
			// with devmode we try to find assertions but it's ok
			// if they are not there (implies --dangerous)
			if !devmode {
				msg := "cannot find signatures with metadata for snap"
				if origPath != "" {
					msg = fmt.Sprintf("%s %q", msg, origPath)
				}
				return BadRequest(msg)
			}
			// TODO: set a warning if devmode
		default:
			return BadRequest(err.Error())
		}
	}

	if snapName == "" {
		// potentially dangerous but dangerous or devmode params were set
		info, err := unsafeReadSnapInfo(tempPath)
		if err != nil {
			return InternalError("cannot read snap file: %v", err)
		}
		snapName = info.Name()
		sideInfo = &snap.SideInfo{RealName: snapName}
	}

	msg := fmt.Sprintf(i18n.G("Install %q snap from file"), snapName)
	if origPath != "" {
		msg = fmt.Sprintf(i18n.G("Install %q snap from file %q"), snapName, origPath)
	}

	var userID int
	if user != nil {
		userID = user.ID
	}

	tsets, err := withEnsureUbuntuCore(st, snapName, userID,
		func() (*state.TaskSet, error) {
			return snapstateInstallPath(st, sideInfo, tempPath, "", flags)
		},
	)
	if err != nil {
		return InternalError("cannot install snap file: %v", err)
	}

	chg := newChange(st, "install-snap", msg, tsets, []string{snapName})
	chg.Set("api-data", map[string]string{"snap-name": snapName})

	ensureStateSoon(st)

	return AsyncResponse(nil, &Meta{Change: chg.ID()})
}

func unsafeReadSnapInfoImpl(snapPath string) (*snap.Info, error) {
	// Condider using DeriveSideInfo before falling back to this!
	snapf, err := snap.Open(snapPath)
	if err != nil {
		return nil, err
	}
	return snap.ReadInfoFromSnapFile(snapf, nil)
}

var unsafeReadSnapInfo = unsafeReadSnapInfoImpl

func iconGet(st *state.State, name string) Response {
	info, _, err := localSnapInfo(st, name)
	if err != nil {
		if err == errNoSnap {
			return NotFound("cannot find snap %q", name)
		}
		return InternalError("%v", err)
	}

	path := filepath.Clean(snapIcon(info))
	if !strings.HasPrefix(path, dirs.SnapMountDir) {
		// XXX: how could this happen?
		return BadRequest("requested icon is not in snap path")
	}

	return FileResponse(path)
}

func appIconGet(c *Command, r *http.Request, user *auth.UserState) Response {
	vars := muxVars(r)
	name := vars["name"]

	return iconGet(c.d.overlord.State(), name)
}

func getSnapConf(c *Command, r *http.Request, user *auth.UserState) Response {
	vars := muxVars(r)
	snapName := vars["name"]

	keys := strings.Split(r.URL.Query().Get("keys"), ",")
	if len(keys) == 0 {
		return BadRequest("cannot obtain configuration: no keys supplied")
	}

	s := c.d.overlord.State()
	s.Lock()
	transaction := configstate.NewTransaction(s)
	s.Unlock()

	currentConfValues := make(map[string]interface{})
	for _, key := range keys {
		var value interface{}
		if err := transaction.Get(snapName, key, &value); err != nil {
			return BadRequest("%s", err)
		}

		currentConfValues[key] = value
	}

	return SyncResponse(currentConfValues, nil)
}

func setSnapConf(c *Command, r *http.Request, user *auth.UserState) Response {
	vars := muxVars(r)
	snapName := vars["name"]

	var patchValues map[string]interface{}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&patchValues); err != nil {
		return BadRequest("cannot decode request body into patch values: %v", err)
	}

	s := c.d.overlord.State()
	s.Lock()
	defer s.Unlock()

	taskset := configstate.Change(s, snapName, patchValues)
	change := s.NewChange("configure-snap", fmt.Sprintf("Setting config for %s", snapName))
	change.AddAll(taskset)

	s.EnsureBefore(0)

	return AsyncResponse(nil, &Meta{Change: change.ID()})
}

// getInterfaces returns all plugs and slots.
func getInterfaces(c *Command, r *http.Request, user *auth.UserState) Response {
	repo := c.d.overlord.InterfaceManager().Repository()
	return SyncResponse(repo.Interfaces(), nil)
}

// plugJSON aids in marshaling Plug into JSON.
type plugJSON struct {
	Snap        string                 `json:"snap"`
	Name        string                 `json:"plug"`
	Interface   string                 `json:"interface"`
	Attrs       map[string]interface{} `json:"attrs,omitempty"`
	Apps        []string               `json:"apps,omitempty"`
	Label       string                 `json:"label"`
	Connections []interfaces.SlotRef   `json:"connections,omitempty"`
}

// slotJSON aids in marshaling Slot into JSON.
type slotJSON struct {
	Snap        string                 `json:"snap"`
	Name        string                 `json:"slot"`
	Interface   string                 `json:"interface"`
	Attrs       map[string]interface{} `json:"attrs,omitempty"`
	Apps        []string               `json:"apps,omitempty"`
	Label       string                 `json:"label"`
	Connections []interfaces.PlugRef   `json:"connections,omitempty"`
}

// interfaceAction is an action performed on the interface system.
type interfaceAction struct {
	Action string     `json:"action"`
	Plugs  []plugJSON `json:"plugs,omitempty"`
	Slots  []slotJSON `json:"slots,omitempty"`
}

// changeInterfaces controls the interfaces system.
// Plugs can be connected to and disconnected from slots.
// When enableInternalInterfaceActions is true plugs and slots can also be
// explicitly added and removed.
func changeInterfaces(c *Command, r *http.Request, user *auth.UserState) Response {
	var a interfaceAction
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&a); err != nil {
		return BadRequest("cannot decode request body into an interface action: %v", err)
	}
	if a.Action == "" {
		return BadRequest("interface action not specified")
	}
	if !c.d.enableInternalInterfaceActions && a.Action != "connect" && a.Action != "disconnect" {
		return BadRequest("internal interface actions are disabled")
	}
	if len(a.Plugs) > 1 || len(a.Slots) > 1 {
		return NotImplemented("many-to-many operations are not implemented")
	}
	if a.Action != "connect" && a.Action != "disconnect" {
		return BadRequest("unsupported interface action: %q", a.Action)
	}
	if len(a.Plugs) == 0 || len(a.Slots) == 0 {
		return BadRequest("at least one plug and slot is required")
	}

	var summary string
	var taskset *state.TaskSet
	var err error

	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	switch a.Action {
	case "connect":
		summary = fmt.Sprintf("Connect %s:%s to %s:%s", a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
		taskset, err = ifacestate.Connect(state, a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
	case "disconnect":
		summary = fmt.Sprintf("Disconnect %s:%s from %s:%s", a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
		taskset, err = ifacestate.Disconnect(state, a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
	}
	if err != nil {
		return BadRequest("%v", err)
	}

	change := state.NewChange(a.Action+"-snap", summary)
	change.Set("snap-names", []string{a.Plugs[0].Snap, a.Slots[0].Snap})
	change.AddAll(taskset)

	state.EnsureBefore(0)

	return AsyncResponse(nil, &Meta{Change: change.ID()})
}

func doAssert(c *Command, r *http.Request, user *auth.UserState) Response {
	batch := assertstate.NewBatch()
	_, err := batch.AddStream(r.Body)
	if err != nil {
		return BadRequest("cannot decode request body into assertions: %v", err)
	}

	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	if err := batch.Commit(state); err != nil {
		return BadRequest("assert failed: %v", err)
	}
	// TODO: what more info do we want to return on success?
	return &resp{
		Type:   ResponseTypeSync,
		Status: http.StatusOK,
	}
}

func assertsFindMany(c *Command, r *http.Request, user *auth.UserState) Response {
	assertTypeName := muxVars(r)["assertType"]
	assertType := asserts.Type(assertTypeName)
	if assertType == nil {
		return BadRequest("invalid assert type: %q", assertTypeName)
	}
	headers := map[string]string{}
	q := r.URL.Query()
	for k := range q {
		headers[k] = q.Get(k)
	}

	state := c.d.overlord.State()
	state.Lock()
	db := assertstate.DB(state)
	state.Unlock()

	assertions, err := db.FindMany(assertType, headers)
	if err == asserts.ErrNotFound {
		return AssertResponse(nil, true)
	} else if err != nil {
		return InternalError("searching assertions failed: %v", err)
	}
	return AssertResponse(assertions, true)
}

func getEvents(c *Command, r *http.Request, user *auth.UserState) Response {
	return EventResponse(c.d.hub)
}

type changeInfo struct {
	ID      string      `json:"id"`
	Kind    string      `json:"kind"`
	Summary string      `json:"summary"`
	Status  string      `json:"status"`
	Tasks   []*taskInfo `json:"tasks,omitempty"`
	Ready   bool        `json:"ready"`
	Err     string      `json:"err,omitempty"`

	SpawnTime time.Time  `json:"spawn-time,omitempty"`
	ReadyTime *time.Time `json:"ready-time,omitempty"`

	Data map[string]*json.RawMessage `json:"data,omitempty"`
}

type taskInfo struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Summary  string           `json:"summary"`
	Status   string           `json:"status"`
	Log      []string         `json:"log,omitempty"`
	Progress taskInfoProgress `json:"progress"`

	SpawnTime time.Time  `json:"spawn-time,omitempty"`
	ReadyTime *time.Time `json:"ready-time,omitempty"`
}

type taskInfoProgress struct {
	Label string `json:"label"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

func change2changeInfo(chg *state.Change) *changeInfo {
	status := chg.Status()
	chgInfo := &changeInfo{
		ID:      chg.ID(),
		Kind:    chg.Kind(),
		Summary: chg.Summary(),
		Status:  status.String(),
		Ready:   status.Ready(),

		SpawnTime: chg.SpawnTime(),
	}
	readyTime := chg.ReadyTime()
	if !readyTime.IsZero() {
		chgInfo.ReadyTime = &readyTime
	}
	if err := chg.Err(); err != nil {
		chgInfo.Err = err.Error()
	}

	tasks := chg.Tasks()
	taskInfos := make([]*taskInfo, len(tasks))
	for j, t := range tasks {
		label, done, total := t.Progress()

		taskInfo := &taskInfo{
			ID:      t.ID(),
			Kind:    t.Kind(),
			Summary: t.Summary(),
			Status:  t.Status().String(),
			Log:     t.Log(),
			Progress: taskInfoProgress{
				Label: label,
				Done:  done,
				Total: total,
			},
			SpawnTime: t.SpawnTime(),
		}
		readyTime := t.ReadyTime()
		if !readyTime.IsZero() {
			taskInfo.ReadyTime = &readyTime
		}
		taskInfos[j] = taskInfo
	}
	chgInfo.Tasks = taskInfos

	var data map[string]*json.RawMessage
	if chg.Get("api-data", &data) == nil {
		chgInfo.Data = data
	}

	return chgInfo
}

func getChange(c *Command, r *http.Request, user *auth.UserState) Response {
	chID := muxVars(r)["id"]
	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()
	chg := state.Change(chID)
	if chg == nil {
		return NotFound("cannot find change with id %q", chID)
	}

	return SyncResponse(change2changeInfo(chg), nil)
}

func getChanges(c *Command, r *http.Request, user *auth.UserState) Response {
	query := r.URL.Query()
	qselect := query.Get("select")
	if qselect == "" {
		qselect = "in-progress"
	}
	var filter func(*state.Change) bool
	switch qselect {
	case "all":
		filter = func(*state.Change) bool { return true }
	case "in-progress":
		filter = func(chg *state.Change) bool { return !chg.Status().Ready() }
	case "ready":
		filter = func(chg *state.Change) bool { return chg.Status().Ready() }
	default:
		return BadRequest("select should be one of: all,in-progress,ready")
	}

	if wantedName := query.Get("for"); wantedName != "" {
		outerFilter := filter
		filter = func(chg *state.Change) bool {
			if !outerFilter(chg) {
				return false
			}

			var snapNames []string
			if err := chg.Get("snap-names", &snapNames); err != nil {
				logger.Noticef("Cannot get snap-name for change %v", chg.ID())
				return false
			}

			for _, snapName := range snapNames {
				if snapName == wantedName {
					return true
				}
			}

			return false
		}
	}

	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()
	chgs := state.Changes()
	chgInfos := make([]*changeInfo, 0, len(chgs))
	for _, chg := range chgs {
		if !filter(chg) {
			continue
		}
		chgInfos = append(chgInfos, change2changeInfo(chg))
	}
	return SyncResponse(chgInfos, nil)
}

func abortChange(c *Command, r *http.Request, user *auth.UserState) Response {
	chID := muxVars(r)["id"]
	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()
	chg := state.Change(chID)
	if chg == nil {
		return NotFound("cannot find change with id %q", chID)
	}

	var reqData struct {
		Action string `json:"action"`
	}

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&reqData); err != nil {
		return BadRequest("cannot decode data from request body: %v", err)
	}

	if reqData.Action != "abort" {
		return BadRequest("change action %q is unsupported", reqData.Action)
	}

	if chg.Status().Ready() {
		return BadRequest("cannot abort change %s with nothing pending", chID)
	}

	// flag the change
	chg.Abort()

	// actually ask to proceed with the abort
	ensureStateSoon(state)

	return SyncResponse(change2changeInfo(chg), nil)
}

var (
	postCreateUserUcrednetGetUID = ucrednetGetUID
	storeUserInfo                = store.UserInfo
	osutilAddUser                = osutil.AddUser
)

func getUserDetailsFromStore(email string) (string, *osutil.AddUserOptions, error) {
	v, err := storeUserInfo(email)
	if err != nil {
		return "", nil, fmt.Errorf("cannot create user %q: %s", email, err)
	}
	if len(v.SSHKeys) == 0 {
		return "", nil, fmt.Errorf("cannot create user for %q: no ssh keys found", email)
	}

	gecos := fmt.Sprintf("%s,%s", email, v.OpenIDIdentifier)
	opts := &osutil.AddUserOptions{
		SSHKeys: v.SSHKeys,
		Gecos:   gecos,
	}
	return v.Username, opts, nil
}

func getUserDetailsFromAssertion(st *state.State, email string) (string, *osutil.AddUserOptions, error) {
	errorPrefix := fmt.Sprintf("cannot add system-user %q: ", email)

	st.Lock()
	db := assertstate.DB(st)
	modelAs, err := devicestate.Model(st)
	st.Unlock()
	if err != nil {
		return "", nil, fmt.Errorf(errorPrefix+"cannot get model assertion: %s", err)
	}

	brandID := modelAs.BrandID()
	series := modelAs.Series()
	model := modelAs.Model()

	a, err := db.Find(asserts.SystemUserType, map[string]string{
		"brand-id": brandID,
		"email":    email,
	})
	if err != nil {
		return "", nil, fmt.Errorf(errorPrefix+"%v", err)
	}
	// the asserts package guarantees that this cast will work
	su := a.(*asserts.SystemUser)

	// cross check that the assertion is valid for the given series/model
	contains := func(needle string, haystack []string) bool {
		for _, s := range haystack {
			if needle == s {
				return true
			}
		}
		return false
	}
	if len(su.Series()) > 0 && !contains(series, su.Series()) {
		return "", nil, fmt.Errorf(errorPrefix+"%q not in series %q", email, series, su.Series())
	}
	if len(su.Models()) > 0 && !contains(model, su.Models()) {
		return "", nil, fmt.Errorf(errorPrefix+"%q not in models %q", model, su.Models())
	}
	if !su.ValidAt(time.Now()) {
		return "", nil, fmt.Errorf(errorPrefix + "assertion not valid anymore")
	}

	gecos := fmt.Sprintf("%s,%s", email, su.Name())
	opts := &osutil.AddUserOptions{
		SSHKeys:  su.SSHKeys(),
		Gecos:    gecos,
		Password: su.Password(),
	}
	return su.Username(), opts, nil
}

type createResponseData struct {
	Username string   `json:"username"`
	SSHKeys  []string `json:"ssh-keys"`
	// deprecated
	SSHKeyCount int `json:"ssh-key-count"`
}

func postCreateUser(c *Command, r *http.Request, user *auth.UserState) Response {
	uid, err := postCreateUserUcrednetGetUID(r.RemoteAddr)
	if err != nil {
		return BadRequest("cannot get ucrednet uid: %v", err)
	}
	if uid != 0 {
		return BadRequest("cannot use create-user as non-root")
	}

	var createData struct {
		Email  string `json:"email"`
		Sudoer bool   `json:"sudoer"`
		Known  bool   `json:"known"`
	}

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&createData); err != nil {
		return BadRequest("cannot decode create-user data from request body: %v", err)
	}

	if createData.Email == "" {
		return BadRequest("cannot create user: 'email' field is empty")
	}

	var username string
	var opts *osutil.AddUserOptions
	if createData.Known {
		username, opts, err = getUserDetailsFromAssertion(c.d.overlord.State(), createData.Email)
	} else {
		username, opts, err = getUserDetailsFromStore(createData.Email)
	}
	if err != nil {
		return BadRequest("%s", err)
	}
	opts.Sudoer = createData.Sudoer
	opts.ExtraUsers = !release.OnClassic

	if err := osutilAddUser(username, opts); err != nil {
		return BadRequest("cannot create user %s: %s", username, err)
	}

	return SyncResponse(&createResponseData{
		Username:    username,
		SSHKeys:     opts.SSHKeys,
		SSHKeyCount: len(opts.SSHKeys),
	}, nil)
}

func postBuy(c *Command, r *http.Request, user *auth.UserState) Response {
	var opts store.BuyOptions

	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&opts)
	if err != nil {
		return BadRequest("cannot decode buy options from request body: %v", err)
	}

	s := getStore(c)

	buyResult, err := s.Buy(&opts, user)

	switch err {
	default:
		return InternalError("%v", err)
	case store.ErrInvalidCredentials:
		return Unauthorized(err.Error())
	case nil:
		// continue
	}

	return SyncResponse(buyResult, nil)
}

// TODO Remove once the CLI is using the new /buy/ready endpoint
func getPaymentMethods(c *Command, r *http.Request, user *auth.UserState) Response {
	s := getStore(c)

	paymentMethods, err := s.PaymentMethods(user)

	switch err {
	default:
		return InternalError("%v", err)
	case store.ErrInvalidCredentials:
		return Unauthorized(err.Error())
	case nil:
		// continue
	}

	return SyncResponse(paymentMethods, nil)
}

func readyToBuy(c *Command, r *http.Request, user *auth.UserState) Response {
	s := getStore(c)

	err := s.ReadyToBuy(user)

	switch err {
	default:
		return InternalError("%v", err)
	case store.ErrInvalidCredentials:
		return Unauthorized(err.Error())
	case store.ErrTOSNotAccepted:
		return SyncResponse(&resp{
			Type: ResponseTypeError,
			Result: &errorResult{
				Message: err.Error(),
				Kind:    errorKindTermsNotAccepted,
			},
			Status: http.StatusBadRequest,
		}, nil)
	case store.ErrNoPaymentMethods:
		return SyncResponse(&resp{
			Type: ResponseTypeError,
			Result: &errorResult{
				Message: err.Error(),
				Kind:    errorKindNoPaymentMethods,
			},
			Status: http.StatusBadRequest,
		}, nil)
	case nil:
		// continue
	}

	return SyncResponse(true, nil)
}

func runSnapctl(c *Command, r *http.Request, user *auth.UserState) Response {
	var snapctlOptions client.SnapCtlOptions
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&snapctlOptions); err != nil {
		return BadRequest("cannot decode snapctl request: %s", err)
	}

	if len(snapctlOptions.Args) == 0 {
		return BadRequest("snapctl cannot run without args")
	}

	// Right now snapctl is only used for hooks. If at some point it grows
	// beyond that, this probably shouldn't go straight to the HookManager.
	context, _ := c.d.overlord.HookManager().Context(snapctlOptions.ContextID)
	stdout, stderr, err := ctlcmd.Run(context, snapctlOptions.Args)
	if err != nil {
		if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrHelp {
			stdout = []byte(e.Error())
		} else {
			return BadRequest("error running snapctl: %s", err)
		}
	}

	result := map[string]string{
		"stdout": string(stdout),
		"stderr": string(stderr),
	}

	return SyncResponse(result, nil)
}
