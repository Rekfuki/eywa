package server

import (
	"net"
	"net/http"
	"time"

	"github.com/fvbock/endless"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/miketonks/swag"
	sv "github.com/miketonks/swag-validator"
	"github.com/miketonks/swag/swagger"
	uuid "github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
	"gopkg.in/resty.v1"

	"eywa/gateway/api/controllers"
	"eywa/gateway/clients/k8s"
	"eywa/gateway/clients/registry"
	"eywa/gateway/metrics"
	"eywa/gateway/types"
	"eywa/go-libs/auth"
	"eywa/go-libs/broker"
)

// ContextParams holds the objects required to initialise the server.
type ContextParams struct {
	K8s      *k8s.Client
	Metrics  *metrics.Client
	Registry *registry.Client
	Broker   *broker.Client
}

func contextObjects(contextParams *ContextParams) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			rc := resty.New()
			rc.SetTimeout(10 * time.Second)
			rc.SetRedirectPolicy(resty.NoRedirectPolicy())
			rc.SetTransport(&http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 1 * time.Second,
					DualStack: true,
				}).DialContext,
				MaxIdleConns:          1024,
				MaxIdleConnsPerHost:   1024,
				IdleConnTimeout:       120 * time.Millisecond,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1500 * time.Millisecond,
			})

			c.Set("proxy", rc)
			c.Set("k8s", contextParams.K8s)
			c.Set("metrics", contextParams.Metrics)
			c.Set("registry", contextParams.Registry)
			c.Set("broker", contextParams.Broker)
			return next(c)
		}
	}
}

func checkAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("X-Request-Id") == "" {
				id, _ := uuid.NewV4()
				c.Request().Header.Set("X-Request-Id", id.String())
			}

			auth := auth.FromHeaders(c.Request().Header)
			if !auth.Check() {
				return c.JSON(http.StatusForbidden, "Forbidden")
			}
			c.Set("auth", auth)
			return next(c)
		}
	}
}

func zeroScale() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {

			auth := c.Get("auth").(*auth.Auth)
			k8sClient := c.Get("k8s").(*k8s.Client)
			functionID := c.Param("function_id")

			filter := k8s.LabelSelector().
				Equals(types.FunctionIDLabel, functionID).
				Equals(types.UserIDLabel, auth.UserID)

			scaleResult, err := k8sClient.ScaleFromZero(filter)
			if err != nil {
				log.Errorf("Error scaling function from zero: %s", err)
				return c.JSON(http.StatusInternalServerError, "Internal Server Error")
			}

			if !scaleResult.Found {
				log.Debugf("Function %q deployment not found")
				return c.JSON(http.StatusNotFound, "Function not found")
			}

			if !scaleResult.Available {
				log.Errorf("Function %q scale request timed-out after %fs", functionID, scaleResult.Duration)
			}

			// Should always exist
			functionName := "UNKNOWN"
			if val, exists := scaleResult.FunctionStatus.Labels[types.UserDefinedNameLabel]; exists {
				functionName = val
			}

			c.Set("function_name", functionName)
			return next(c)
		}
	}
}

// Run starts the api server.
func Run(params *ContextParams) {
	r := createRouter(params)

	endless.DefaultHammerTime = 10 * time.Second
	endless.DefaultReadTimeOut = 295 * time.Second
	if err := endless.ListenAndServe(":8080", r); err != nil {
		log.Infof("Server stopped: %s", err)
	}
}

func createRouter(params *ContextParams) *echo.Echo {
	e := echo.New()
	e.Use(middleware.Recover())
	e.Use(contextObjects(params))

	log.SetLevel(log.ErrorLevel)

	// Expose metrics for prometheus
	e.GET("/metrics", echo.WrapHandler(params.Metrics.PrometheusHandler()))

	e.POST("/eywa/api/system/alert", controllers.InvocationAlert)

	// Proxy direct function calls
	syncMethods := []string{"POST", "PUT", "PATCH", "DELETE", "GET"}
	e.Match(syncMethods, "/eywa/api/functions/sync/:function_id/*path", controllers.Proxy, checkAuth(), zeroScale())
	e.POST("/eywa/api/functions/async/:function_id/*path", controllers.AsyncInvocation, checkAuth())

	enableCors := true
	gatewayAPI := createGatewayAPI()
	e.GET("/eywa/api/gateway/doc", echo.WrapHandler(gatewayAPI.Handler(enableCors)))

	api := e.Group("", checkAuth(), sv.SwaggerValidatorEcho(gatewayAPI))
	gatewayAPI.Walk(func(path string, endpoint *swagger.Endpoint) {
		h := endpoint.Handler.(func(c echo.Context) error)
		path = swag.ColonPath(path)
		api.Add(endpoint.Method, path, h)
	})

	systemAPI := createSystemAPI()
	e.GET("/eywa/api/system/gateway/doc", echo.WrapHandler(systemAPI.Handler(enableCors)))

	api = e.Group("", checkAuth(), sv.SwaggerValidatorEcho(systemAPI))
	systemAPI.Walk(func(path string, endpoint *swagger.Endpoint) {
		h := endpoint.Handler.(func(c echo.Context) error)
		path = swag.ColonPath(path)
		api.Add(endpoint.Method, path, h)
	})

	return e
}

func createGatewayAPI() *swagger.API {
	return swag.New(
		swag.Title("Gateway"),
		swag.Description(`Gateway API`),
		swag.Version("2.0"),
		swag.BasePath("/eywa/api"),
		swag.Endpoints(aggregateEndpoints(
			functionsAPI(),
			secretsAPI(),
			metricsAPI(),
		)...,
		),
	)
}

func createSystemAPI() *swagger.API {
	return swag.New(
		swag.Title("Gateway System"),
		swag.Description(`Gateway System API`),
		swag.Version("2.0"),
		swag.BasePath("/eywa/api"),
		swag.Endpoints(aggregateEndpoints(
			systemAPI(),
		)...,
		),
	)
}

func aggregateEndpoints(endpoints ...[]*swagger.Endpoint) (res []*swagger.Endpoint) {
	for _, v := range endpoints {
		res = append(res, v...)
	}

	return
}
