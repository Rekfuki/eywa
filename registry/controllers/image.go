package controllers

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	uuid "github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"

	"eywa/go-libs/auth"
	"eywa/registry/builder"
	"eywa/registry/db"
	"eywa/registry/types"
)

// GetImages returns all the images a user can access
func GetImages(c echo.Context) error {
	db := c.Get("db").(*db.Client)
	auth := c.Get("auth").(*auth.Auth)
	page := c.Get("page_number").(int)
	perPage := c.Get("per_page").(int)
	query := c.QueryParam("query")

	images, total, err := db.GetImagesWithoutSource(auth.UserID, query, page, perPage)
	if err != nil {
		log.Errorf("Failed to retrieve images: %s", err)
		return c.JSON(http.StatusInternalServerError, "Internal Server Error")
	}

	return c.JSON(http.StatusOK, types.GetImagesResponse{
		Objects: images,
		Total:   total,
		Page:    page,
		PerPage: perPage,
	})
}

// GetImage returns a specific image
func GetImage(c echo.Context) error {
	db := c.Get("db").(*db.Client)
	auth := c.Get("auth").(*auth.Auth)
	imageID := c.Param("image_id")

	image, err := db.GetImageWithoutSource(imageID, auth.UserID)
	if err != nil {
		log.Errorf("Failed to retrieve image: %s", err)
		return err
	}

	if image == nil {
		return c.JSON(http.StatusNotFound, "Not Found")
	}

	if !auth.IsOperator() {
		image.TaggedRegistry = ""
	}

	return c.JSON(http.StatusOK, image)
}

// RequestImageBuild queues up a new image build
func RequestImageBuild(c echo.Context) error {
	db := c.Get("db").(*db.Client)
	bc := c.Get("builder").(*builder.Client)
	auth := c.Get("auth").(*auth.Auth)

	file, err := c.FormFile("source")
	if err != nil {
		log.Errorf("Failed to get source from payload: %s", err)
		return err
	}

	runtime := strings.ToLower(c.FormValue("runtime"))
	version := c.FormValue("version")
	name := c.FormValue("name")

	var executablePath *string
	if runtime == "custom" {
		executableString := c.FormValue("executable_path")
		if executableString == "" {
			return c.JSON(http.StatusBadRequest, "Executable is required when using custom mode")
		}
		executablePath = &executableString
	}

	fullName := fmt.Sprintf("%s##%s##%s", runtime, name, version)
	id := uuid.NewV5(uuid.FromStringOrNil(auth.UserID), fullName).String()

	existingImage, err := db.GetImageWithoutSource(id, auth.UserID)
	if err != nil {
		log.Errorf("Failed to retrieve image from db: %s", err)
		return err
	}

	if existingImage != nil {
		return c.JSON(http.StatusConflict, "Exact same image already exists")
	}

	src, err := file.Open()
	if err != nil {
		log.Errorf("Failed to open file header for reading: %s", err)
		return err
	}
	defer src.Close()

	body, err := ioutil.ReadAll(src)
	if err != nil {
		log.Errorf("Failed to read body: %s", err)
		return err
	}

	builderErr := bc.Enqueue(builder.BuildRequest{
		ImageID:        id,
		UserID:         auth.UserID,
		Name:           name,
		Runtime:        runtime,
		Version:        version,
		ZippedSource:   body,
		ExecutablePath: executablePath,
	})
	if builderErr != nil {
		if builderErr.Type == builder.ErrTypeUserError {
			return c.JSON(http.StatusBadRequest, builderErr.String())
		}

		log.Errorf("Failed enqueue build: %s", builderErr.String())
		c.JSON(http.StatusInternalServerError, "Internal Server Error")
	}

	return c.JSON(http.StatusOK, types.ImageBuildResponse{
		BuildID:   id,
		CreatedAt: time.Now(),
	})
}

// GetImageBuildLogs streams image build logs either live or from the db
func GetImageBuildLogs(c echo.Context) error {
	db := c.Get("db").(*db.Client)
	bc := c.Get("builder").(*builder.Client)
	auth := c.Get("auth").(*auth.Auth)
	imageID := c.Param("image_id")

	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set(echo.HeaderXContentTypeOptions, "nosniff")
	c.Response().WriteHeader(http.StatusOK)

	existingBuild := bc.GetBuild(imageID, auth.UserID)
	if existingBuild != nil {
		logs := []string{}
		logFile, err := os.OpenFile(existingBuild.LogFile, os.O_RDONLY, 0666)
		if err != nil {
			log.Errorf("Failed to read logs file: %s", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
		}
		logFile.Seek(0, io.SeekStart)

		scanner := bufio.NewScanner(logFile)
		scanner.Split(bufio.ScanLines)
		for scanner.Scan() {
			logs = append(logs, scanner.Text())
		}

		return c.JSON(http.StatusOK, types.ImageLogs{Logs: logs})
	} else {
		dbBuild, err := db.GetBuild(imageID, auth.UserID)
		if err != nil {
			log.Errorf("Failed to get build from db: %s", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
		}

		if dbBuild == nil {
			return c.JSON(http.StatusNotFound, "No build logs found")
		}
		return c.JSON(http.StatusOK, types.ImageLogs{Logs: dbBuild.Logs})
	}
}

// DeleteImage deletes the image from db and registry
func DeleteImage(c echo.Context) error {
	db := c.Get("db").(*db.Client)
	builder := c.Get("builder").(*builder.Client)
	auth := c.Get("auth").(*auth.Auth)
	imageID := c.Param("image_id")

	if inProgressBuild := builder.GetBuild(imageID, auth.UserID); inProgressBuild != nil {
		return c.JSON(http.StatusBadRequest, "Cannot delete build in progress")
	}

	image, err := db.GetImageWithoutSource(imageID, auth.UserID)
	if err != nil {
		log.Errorf("Failed to retrieve image: %s", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}

	if image == nil {
		return c.JSON(http.StatusNotFound, "Not Found")
	}

	// Don't delete from docker in case some function is still using it.
	// Docker registry will cleanup eventually.

	if err := db.DeleteBuild(imageID, auth.UserID); err != nil {
		log.Errorf("Failed to delete build info from db: %s", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}

	if err := db.DeleteImage(imageID, auth.UserID); err != nil {
		log.Errorf("Failed to delete image form db: %s", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}

	return c.NoContent(http.StatusNoContent)
}
