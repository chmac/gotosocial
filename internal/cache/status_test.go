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

package cache_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/superseriousbusiness/gotosocial/internal/cache"
	"github.com/superseriousbusiness/gotosocial/internal/gtsmodel"
	"github.com/superseriousbusiness/gotosocial/testrig"
)

type StatusCacheTestSuite struct {
	suite.Suite
	data  map[string]*gtsmodel.Status
	cache *cache.StatusCache
}

func (suite *StatusCacheTestSuite) SetupSuite() {
	suite.data = testrig.NewTestStatuses()
}

func (suite *StatusCacheTestSuite) SetupTest() {
	suite.cache = cache.NewStatusCache()
}

func (suite *StatusCacheTestSuite) TearDownTest() {
	suite.data = nil
	suite.cache = nil
}

func (suite *StatusCacheTestSuite) TestStatusCache() {
	for _, status := range suite.data {
		// Place in the cache
		suite.cache.Put(status)
	}

	for _, status := range suite.data {
		var ok bool
		var check *gtsmodel.Status

		// Check we can retrieve
		check, ok = suite.cache.GetByID(status.ID)
		if !ok && !statusIs(status, check) {
			suite.Fail("Failed to fetch expected account with ID: %s", status.ID)
		}
		check, ok = suite.cache.GetByURI(status.URI)
		if status.URI != "" && !ok && !statusIs(status, check) {
			suite.Fail("Failed to fetch expected account with URI: %s", status.URI)
		}
		check, ok = suite.cache.GetByURL(status.URL)
		if status.URL != "" && !ok && !statusIs(status, check) {
			suite.Fail("Failed to fetch expected account with URL: %s", status.URL)
		}
	}
}

func (suite *StatusCacheTestSuite) TestBoolPointerCopying() {
	originalStatus := suite.data["local_account_1_status_1"]

	// mark the status as pinned + cache it
	pinned := true
	originalStatus.Pinned = &pinned
	suite.cache.Put(originalStatus)

	// retrieve it
	cachedStatus, ok := suite.cache.GetByID(originalStatus.ID)
	if !ok {
		suite.FailNow("status wasn't retrievable from cache")
	}

	// we should be able to change the original status values + cached
	// values independently since they use different pointers
	suite.True(*cachedStatus.Pinned)
	*originalStatus.Pinned = false
	suite.False(*originalStatus.Pinned)
	suite.True(*cachedStatus.Pinned)
	*originalStatus.Pinned = true
	*cachedStatus.Pinned = false
	suite.True(*originalStatus.Pinned)
	suite.False(*cachedStatus.Pinned)
}

func TestStatusCache(t *testing.T) {
	suite.Run(t, &StatusCacheTestSuite{})
}

func statusIs(status1, status2 *gtsmodel.Status) bool {
	return status1.ID == status2.ID && status1.URI == status2.URI && status1.URL == status2.URL
}
