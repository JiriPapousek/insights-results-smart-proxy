// Copyright 2023 Red Hat, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	httputils "github.com/RedHatInsights/insights-operator-utils/http"
	"github.com/RedHatInsights/insights-operator-utils/responses"
	utypes "github.com/RedHatInsights/insights-operator-utils/types"
	"github.com/RedHatInsights/insights-results-smart-proxy/types"

	"github.com/rs/zerolog/log"
)

// UpgradeRisksPredictionServiceEndpoint endpoint for the upgrade prediction service
const UpgradeRisksPredictionServiceEndpoint = "cluster/{cluster}/upgrade-risks-prediction"

// method upgradeRisksPrediction returns a recommendation to upgrade or not a cluster
// and a list of the alerts/operator conditions that were taken into account if the
// upgrade is not recommended.
//
// Response format should look like:
//
//	{
//		"upgrade_recommended": false,
//		"upgrade_risks_predictors": {
//			"alerts": [
//				{
//					"name": "APIRemovedInNextEUSReleaseInUse",
//					"namespace": "openshift-kube-apiserver",
//					"severity": "info"
//				}
//			],
//			"operator_conditions": [
//				{
//					"name": "authentication",
//					"condition": "Failing",
//					"reason": "AsExpected"
//				}
//			]
//		}
//	}
func (server *HTTPServer) upgradeRisksPrediction(writer http.ResponseWriter, request *http.Request) {
	if server.amsClient == nil {
		log.Error().Msgf("AMS API connection is not initialized")
		handleServerError(writer, &AMSAPIUnavailableError{})
		return
	}

	orgID, err := server.GetCurrentOrgID(request)
	if err != nil {
		handleServerError(writer, err)
		return
	}

	clusterID, successful := httputils.ReadClusterName(writer, request)
	// error handled by function
	if !successful {
		return
	}

	clusterInfo, err := server.amsClient.GetSingleClusterInfoForOrganization(orgID, clusterID)

	if err != nil {
		log.Error().Err(err).Str(clusterIDTag, string(clusterID)).Msg("failure retrieving the cluster's organization")
		handleServerError(writer, err)
	} else if clusterInfo.ID != clusterID {
		log.Error().Err(err).Str(clusterIDTag, string(clusterID)).Msg("cluster doesn't belong to the expected org")
		handleServerError(writer, &utypes.ItemNotFoundError{ItemID: clusterID})
		return
	}

	// Request to Data Engineering Service to retrieve the result
	predictionResponse, err := server.fetchUpgradePrediction(clusterID, writer)
	if err != nil || predictionResponse == nil {
		// Error already handled or not OK status, already returned
		return
	}

	response := make(map[string]interface{})
	response["upgrade_recommendation"] = predictionResponse
	response["status"] = OkMsg

	// TODO: Currently DataEng service doesn't return any timestamp
	// Getting current time to avoid returning an empty string
	response["meta"] = types.UpgradeRisksMeta{
		LastCheckedAt: types.Timestamp(time.Now().UTC().Format(time.RFC3339)),
	}

	err = responses.SendOK(
		writer,
		response,
	)
	if err != nil {
		log.Error().Err(err).Msg(responseDataError)
	}
}

func (server *HTTPServer) fetchUpgradePrediction(
	cluster types.ClusterName,
	writer http.ResponseWriter,
) (*types.UpgradeRecommendation, error) {
	dataEngURL := httputils.MakeURLToEndpoint(
		server.ServicesConfig.UpgradeRisksPredictionEndpoint,
		UpgradeRisksPredictionServiceEndpoint,
		cluster,
	)

	httpClient := http.Client{
		Timeout: 5 * time.Second,
	}

	// #nosec G107
	response, err := httpClient.Get(dataEngURL)
	if err != nil {
		log.Error().Str(clusterIDTag, string(cluster)).Err(err).Msg("fetchUpgradePrediction unexpected error for cluster")
		handleServerError(writer, err)
		return nil, err
	}

	responseBytes, err := io.ReadAll(response.Body)
	if err != nil {
		handleServerError(writer, err)
		return nil, err
	}

	if response.StatusCode != http.StatusOK {
		err := responses.Send(response.StatusCode, writer, responseBytes)
		if err != nil {
			log.Error().Err(err).Msg(responseDataError)
		}
		return nil, err
	}
	responseData := &types.UpgradeRecommendation{}
	err = json.Unmarshal(responseBytes, &responseData)
	if err != nil {
		log.Error().Str(clusterIDTag, string(cluster)).Err(err).Msg("error unmarshalling data-engineering response")
		handleServerError(writer, err)
		return nil, err
	}

	return responseData, nil
}
