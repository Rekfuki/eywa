package controllers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
	"gopkg.in/resty.v1"

	"eywa/gateway/clients/k8s"
)

func Proxy(c echo.Context) error {
	switch c.Request().Method {
	case http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodGet:

		return proxyRequest(c)
	default:
		return c.JSON(http.StatusMethodNotAllowed, nil)
	}
}

func proxyRequest(c echo.Context) error {
	proxyClient := c.Get("proxy").(*resty.Client)
	k8s := c.Get("k8s").(*k8s.Client)

	functionName := c.Param("name")
	if functionName == "" {
		return c.JSON(http.StatusBadRequest, "Missing function name")
	}

	functionAddr, err := k8s.Resolve(functionName)
	if err != nil {
		log.Errorf("k8s error: cannot find %s: %s\n", functionName, err)
		return c.JSON(http.StatusNotFound, fmt.Sprintf("Cannot find service: %s", functionName))
	}

	path := c.Param("*")
	url := fmt.Sprintf("%s/%s", functionAddr, path)
	proxyRequest := proxyClient.R().SetQueryString(c.QueryString())
	if c.Request().Body != nil {
		proxyRequest.Body = c.Request().Body
	}

	copyHeaders(proxyRequest.Header, &c.Request().Header)

	start := time.Now()
	response, err := proxyRequest.Execute(c.Request().Method, url)
	if err != nil {
		log.Errorf("Error with proxy request to: %s, %s\n", proxyRequest.URL, err)
		return c.JSON(http.StatusInternalServerError, "Internal Server Error")
	}
	seconds := time.Since(start)

	log.Infof("%s took %f seconds\n", functionName, seconds.Seconds())

	return copyResponse(c, response)
}

func copyHeaders(destination http.Header, source *http.Header) {
	for k, v := range *source {
		vClone := make([]string, len(v))
		copy(vClone, v)
		destination[k] = vClone
	}
}

func copyResponse(c echo.Context, response *resty.Response) error {
	h := c.Response().Header()
	for k, v := range response.Header() {
		h[k] = v
	}
	return c.Blob(response.StatusCode(), response.Header().Get("Content-Type"), response.Body())
}