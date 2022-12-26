package scalers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-logr/logr"
	kedautil "github.com/kedacore/keda/v2/pkg/util"
	v2 "k8s.io/api/autoscaling/v2"
	"k8s.io/metrics/pkg/apis/external_metrics"
)

type weatherScaler struct {
	metricType v2.MetricTargetType
	metadata   *weatherMetadata
	httpClient *http.Client
	logger     logr.Logger
}

type weatherMetadata struct {
	host                string
	cityName            string
	apiKey              string
	preference          string  //Temp
	threshold           float64 //A threshold used as the targetAverageValue in the HPA.
	activationThreshold float64 //A threshold used to control whether the scaler is enabled.
	scalerIndex         int
	unsafeSsl           bool
}

type weatherDataResponse struct {
	Main struct {
		Temp     float64 `json:"temp"`
		Temp_min float64 `json:"temp_min"`
		Temp_max float64 `json:"temp_max"`
	} `json:"main"`
}

// NewWeatherScaler creates a new WeatherScaler Scaler
func NewWeatherScaler(config *ScalerConfig) (Scaler, error) {
	metricType, err := GetMetricTargetType(config)
	if err != nil {
		return nil, fmt.Errorf("error getting scaler metric type: %s", err)
	}

	logger := InitializeLogger(config, "weather_scaler")

	meta, err := parseWeatherMetadata(config)

	if err != nil {
		return nil, fmt.Errorf("error parsing weather metadata: %s", err)
	}

	client := kedautil.CreateHTTPClient(config.GlobalHTTPTimeout, meta.unsafeSsl)

	return &weatherScaler{
		metricType: metricType,
		metadata:   meta,
		httpClient: client,
		logger:     logger,
	}, nil
}

func parseWeatherMetadata(config *ScalerConfig) (*weatherMetadata, error) {
	meta := weatherMetadata{}

	meta.unsafeSsl = false
	if val, ok := config.TriggerMetadata["unsafeSsl"]; ok {
		unsafeSsl, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("error parsing unsafeSsl: %s", err)
		}
		meta.unsafeSsl = unsafeSsl
	}

	if val, ok := config.TriggerMetadata["threshold"]; ok {
		threshold, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to convert threshold to int, %w", err)
		}
		meta.threshold = threshold
	} else {
		return nil, fmt.Errorf("no threshold given")
	}

	meta.activationThreshold = 0
	if val, ok := config.TriggerMetadata["activationThreshold"]; ok {
		activationThreshold, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to convert activationThreshold to int, %w", err)
		}
		meta.activationThreshold = activationThreshold
	}

	if config.TriggerMetadata["cityName"] == "" {
		return nil, fmt.Errorf("Missing city name")
	}
	meta.cityName = config.TriggerMetadata["cityName"]

	if config.TriggerMetadata["apiKey"] == "" {
		return nil, fmt.Errorf("No API key given")
	}
	meta.apiKey = config.TriggerMetadata["apiKey"]

	if val, ok := config.TriggerMetadata["host"]; ok {
		urlString := fmt.Sprintf(val, meta.cityName, meta.apiKey)
		_, err := url.ParseRequestURI(urlString)
		if err != nil {
			return nil, fmt.Errorf("Invalid URL: %s", err.Error())
		}
		meta.host = string(urlString)
	} else {
		return nil, fmt.Errorf("Missing URL")
	}

	if config.TriggerMetadata["preference"] == "" {
		return nil, fmt.Errorf("Missing preference ")
	}
	meta.preference = config.TriggerMetadata["preference"]

	meta.scalerIndex = config.ScalerIndex
	//
	return &meta, nil
}

// GetMetricsAndActivity returns value for a supported metric and an error if there is a problem getting the metric
func (s *weatherScaler) GetMetricsAndActivity(ctx context.Context, metricName string) ([]external_metrics.ExternalMetricValue, bool, error) {
	tempr, err := s.GetTheWeather()
	if err != nil {
		return []external_metrics.ExternalMetricValue{}, false, fmt.Errorf("error requesting get weather: %s", err)
	}

	metric := GenerateMetricInMili(metricName, float64(tempr))

	return []external_metrics.ExternalMetricValue{metric}, float64(tempr) > s.metadata.activationThreshold, nil
}

// GetMetricSpecForScaling returns the MetricSpec for the Horizontal Pod Autoscaler
func (s *weatherScaler) GetMetricSpecForScaling(context.Context) []v2.MetricSpec {
	externalMetric := &v2.ExternalMetricSource{
		Metric: v2.MetricIdentifier{
			Name: GenerateMetricNameWithIndex(s.metadata.scalerIndex, "weather"),
		},
		Target: GetMetricTargetMili(s.metricType, s.metadata.threshold),
	}
	metricSpec := v2.MetricSpec{
		External: externalMetric, Type: externalMetricType,
	}
	return []v2.MetricSpec{metricSpec}
}

func (s *weatherScaler) Close(context.Context) error {
	return nil
}

func (s *weatherScaler) GetTheWeather() (int64, error) {
	var tempr int64
	var currentWeather weatherDataResponse
	body, err := s.GetJsonData()
	if err != nil {
		return 100, err
	}
	err = json.Unmarshal(body, &currentWeather)
	if err != nil {
		return 100, err
	}
	switch s.metadata.preference {
	case "Temp_min":
		tempr = int64(currentWeather.Main.Temp_min)
	case "Temp_max":
		tempr = int64(currentWeather.Main.Temp_max)
	case "Temp":
		tempr = int64(currentWeather.Main.Temp)
	}
	return tempr, nil
}

func (s *weatherScaler) GetJsonData() ([]byte, error) {
	resp, err := s.httpClient.Get(s.metadata.host)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, err
}
