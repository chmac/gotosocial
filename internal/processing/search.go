/*
   GoToSocial
   Copyright (C) 2021-2022 GoToSocial Authors admin@gotosocial.org

   This program is free software: you can redistribute it and/or modify
   it under the terms of the GNU Affero General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU Affero General Public License for more details.

   You should have received a copy of the GNU Affero General Public License
   along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package processing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"codeberg.org/gruf/go-kv"
	apimodel "github.com/superseriousbusiness/gotosocial/internal/api/model"
	"github.com/superseriousbusiness/gotosocial/internal/config"
	"github.com/superseriousbusiness/gotosocial/internal/db"
	"github.com/superseriousbusiness/gotosocial/internal/federation/dereferencing"
	"github.com/superseriousbusiness/gotosocial/internal/gtserror"
	"github.com/superseriousbusiness/gotosocial/internal/gtsmodel"
	"github.com/superseriousbusiness/gotosocial/internal/log"
	"github.com/superseriousbusiness/gotosocial/internal/oauth"
	"github.com/superseriousbusiness/gotosocial/internal/util"
)

func (p *processor) SearchGet(ctx context.Context, authed *oauth.Auth, search *apimodel.SearchQuery) (*apimodel.SearchResult, gtserror.WithCode) {
	l := log.WithFields(kv.Fields{
		{"query", search.Query},
	}...)

	// tidy up the query and make sure it wasn't just spaces
	query := strings.TrimSpace(search.Query)
	if query == "" {
		err := errors.New("search query was empty string after trimming space")
		return nil, gtserror.NewErrorBadRequest(err, err.Error())
	}

	searchResult := &apimodel.SearchResult{
		Accounts: []apimodel.Account{},
		Statuses: []apimodel.Status{},
		Hashtags: []apimodel.Tag{},
	}
	foundAccounts := []*gtsmodel.Account{}
	foundStatuses := []*gtsmodel.Status{}

	var foundOne bool

	/*
		SEARCH BY MENTION
		check if the query is something like @whatever_username@example.org -- this means it's likely a remote account
	*/
	maybeNamestring := query
	if maybeNamestring[0] != '@' {
		maybeNamestring = "@" + maybeNamestring
	}

	if username, domain, err := util.ExtractNamestringParts(maybeNamestring); err == nil {
		l.Debugf("search term %s is a mention, looking it up...", maybeNamestring)
		if foundAccount, err := p.searchAccountByMention(ctx, authed, username, domain, search.Resolve); err == nil && foundAccount != nil {
			foundAccounts = append(foundAccounts, foundAccount)
			foundOne = true
			l.Debug("got an account by searching by mention")
		} else if err != nil {
			l.Debugf("error looking up account %s: %s", maybeNamestring, err)
		}
	}

	/*
		SEARCH BY URI
		check if the query is a URI with a recognizable scheme and dereference it
	*/
	if !foundOne {
		if uri, err := url.Parse(query); err == nil && (uri.Scheme == "https" || uri.Scheme == "http") {
			// don't attempt to resolve (ie., dereference) local accounts/statuses
			resolve := search.Resolve
			if uri.Host == config.GetHost() || uri.Host == config.GetAccountDomain() {
				resolve = false
			}

			// check if it's a status or an account
			if foundStatus, err := p.searchStatusByURI(ctx, authed, uri, resolve); err == nil && foundStatus != nil {
				foundStatuses = append(foundStatuses, foundStatus)
				l.Debug("got a status by searching by URI")
			} else if foundAccount, err := p.searchAccountByURI(ctx, authed, uri, resolve); err == nil && foundAccount != nil {
				foundAccounts = append(foundAccounts, foundAccount)
				l.Debug("got an account by searching by URI")
			}
		}
	}

	/*
		FROM HERE ON we have our search results, it's just a matter of filtering them according to what this user is allowed to see,
		and then converting them into our frontend format.
	*/
	for _, foundAccount := range foundAccounts {
		// make sure there's no block in either direction between the account and the requester
		if blocked, err := p.db.IsBlocked(ctx, authed.Account.ID, foundAccount.ID, true); err == nil && !blocked {
			// all good, convert it and add it to the results
			if apiAcct, err := p.tc.AccountToAPIAccountPublic(ctx, foundAccount); err == nil && apiAcct != nil {
				searchResult.Accounts = append(searchResult.Accounts, *apiAcct)
			}
		}
	}

	for _, foundStatus := range foundStatuses {
		if visible, err := p.filter.StatusVisible(ctx, foundStatus, authed.Account); !visible || err != nil {
			continue
		}

		apiStatus, err := p.tc.StatusToAPIStatus(ctx, foundStatus, authed.Account)
		if err != nil {
			continue
		}

		searchResult.Statuses = append(searchResult.Statuses, *apiStatus)
	}

	return searchResult, nil
}

func (p *processor) searchStatusByURI(ctx context.Context, authed *oauth.Auth, uri *url.URL, resolve bool) (*gtsmodel.Status, error) {
	l := log.WithFields(kv.Fields{
		{"uri", uri.String()},
		{"resolve", resolve},
	}...)

	if maybeStatus, err := p.db.GetStatusByURI(ctx, uri.String()); err == nil {
		return maybeStatus, nil
	} else if maybeStatus, err := p.db.GetStatusByURL(ctx, uri.String()); err == nil {
		return maybeStatus, nil
	}

	// we don't have it locally so dereference it if we're allowed to
	if resolve {
		status, _, err := p.federator.GetRemoteStatus(ctx, authed.Account.Username, uri, false, true)
		if err == nil {
			if err := p.federator.DereferenceRemoteThread(ctx, authed.Account.Username, uri); err != nil {
				// try to deref the thread while we're here
				l.Debugf("searchStatusByURI: error dereferencing remote thread: %s", err)
			}
			return status, nil
		}
	}
	return nil, nil
}

func (p *processor) searchAccountByURI(ctx context.Context, authed *oauth.Auth, uri *url.URL, resolve bool) (*gtsmodel.Account, error) {
	// it might be a web url like http://example.org/@user instead
	// of an AP uri like http://example.org/users/user, check first
	if maybeAccount, err := p.db.GetAccountByURL(ctx, uri.String()); err == nil {
		return maybeAccount, nil
	}

	if uri.Host == config.GetHost() || uri.Host == config.GetAccountDomain() {
		// this is a local account; if we don't have it now then
		// we should just bail instead of trying to get it remote
		if maybeAccount, err := p.db.GetAccountByURI(ctx, uri.String()); err == nil {
			return maybeAccount, nil
		}
		return nil, nil
	}

	// we don't have it yet, try to find it remotely
	return p.federator.GetRemoteAccount(ctx, dereferencing.GetRemoteAccountParams{
		RequestingUsername: authed.Account.Username,
		RemoteAccountID:    uri,
		Blocking:           true,
		SkipResolve:        !resolve,
	})
}

func (p *processor) searchAccountByMention(ctx context.Context, authed *oauth.Auth, username string, domain string, resolve bool) (*gtsmodel.Account, error) {
	// if it's a local account we can skip a whole bunch of stuff
	if domain == config.GetHost() || domain == config.GetAccountDomain() || domain == "" {
		maybeAcct, err := p.db.GetLocalAccountByUsername(ctx, username)
		if err == nil || err == db.ErrNoEntries {
			return maybeAcct, nil
		}
		return nil, fmt.Errorf("searchAccountByMention: error getting local account by username: %s", err)
	}

	// we don't have it yet, try to find it remotely
	return p.federator.GetRemoteAccount(ctx, dereferencing.GetRemoteAccountParams{
		RequestingUsername:    authed.Account.Username,
		RemoteAccountUsername: username,
		RemoteAccountHost:     domain,
		Blocking:              true,
		SkipResolve:           !resolve,
	})
}
