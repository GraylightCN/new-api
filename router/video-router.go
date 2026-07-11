package router

import (
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func SetVideoRouter(router *gin.Engine) {
	// Video proxy: accepts either session auth (dashboard) or token auth (API clients)
	videoProxyRouter := router.Group("/v1")
	videoProxyRouter.Use(middleware.RouteTag("relay"))
	videoProxyRouter.Use(middleware.TokenOrUserAuth())
	{
		videoProxyRouter.GET("/videos/:task_id/content", controller.VideoProxy)
	}

	videoV1Router := router.Group("/v1")
	videoV1Router.Use(middleware.RouteTag("relay"))
	videoV1Router.Use(middleware.TokenAuth(), middleware.Distribute())
	{
		videoV1Router.POST("/video/generations", controller.RelayTask)
		videoV1Router.GET("/video/generations/:task_id", controller.RelayTaskFetch)
		videoV1Router.POST("/videos/:video_id/remix", controller.RelayTask)
	}
	// openai compatible API video routes
	// docs: https://platform.openai.com/docs/api-reference/videos/create
	{
		videoV1Router.POST("/videos", controller.RelayTask)
		videoV1Router.GET("/videos/:task_id", controller.RelayTaskFetch)
	}

	klingV1Router := router.Group("/kling/v1")
	klingV1Router.Use(middleware.RouteTag("relay"))
	klingV1Router.Use(middleware.KlingRequestConvert(), middleware.TokenAuth(), middleware.Distribute())
	{
		klingV1Router.POST("/videos/text2video", controller.RelayTask)
		klingV1Router.POST("/videos/image2video", controller.RelayTask)
		klingV1Router.GET("/videos/text2video/:task_id", controller.RelayTaskFetch)
		klingV1Router.GET("/videos/image2video/:task_id", controller.RelayTaskFetch)
	}

	// Volc Ark compatible task routes — native pass-through with unknown fields
	// preserved. These reuse the existing VolcEngine(45) channel: there is no
	// dedicated channel type. Selection of our adaptor is driven by two context
	// values instead of a channel-type number:
	//   - relay_format = "volc" makes RelayTask / RelayTaskFetch take the
	//     Volc-native code path.
	//   - platform = "volc-native" makes RelayTaskSubmit resolve GetTaskAdaptor to
	//     our task adaptor and, critically, persist the task with
	//     Platform="volc-native" so polling (GetTaskAdaptor(originTask.Platform))
	//     routes back here regardless of the underlying channel type.
	volcV3Router := router.Group("/api/v3")
	volcV3Router.Use(middleware.RouteTag("relay"))
	volcV3Router.Use(middleware.TokenAuth(), middleware.Distribute())
	{
		volcV3Router.POST("/contents/generations/tasks", func(c *gin.Context) {
			c.Set("relay_format", string(types.RelayFormatVolc))
			c.Set("platform", string(constant.TaskPlatformVolcNative))
			controller.RelayTask(c)
		})
		volcV3Router.GET("/contents/generations/tasks", func(c *gin.Context) {
			c.Set("relay_format", string(types.RelayFormatVolc))
			c.Set("platform", string(constant.TaskPlatformVolcNative))
			c.Set("relay_mode", relayconstant.RelayModeVideoFetchList)
			controller.RelayTaskFetch(c)
		})
		volcV3Router.GET("/contents/generations/tasks/:id", func(c *gin.Context) {
			c.Set("relay_format", string(types.RelayFormatVolc))
			c.Set("platform", string(constant.TaskPlatformVolcNative))
			c.Set("task_id", c.Param("id"))
			c.Set("relay_mode", relayconstant.RelayModeVideoFetchByID)
			controller.RelayTaskFetch(c)
		})
		volcV3Router.DELETE("/contents/generations/tasks/:id", controller.VolcTaskDelete)
	}

	// Jimeng official API routes - direct mapping to official API format
	jimengOfficialGroup := router.Group("jimeng")
	jimengOfficialGroup.Use(middleware.RouteTag("relay"))
	jimengOfficialGroup.Use(middleware.JimengRequestConvert(), middleware.TokenAuth(), middleware.Distribute())
	{
		// Maps to: /?Action=CVSync2AsyncSubmitTask&Version=2022-08-31 and /?Action=CVSync2AsyncGetResult&Version=2022-08-31
		jimengOfficialGroup.POST("/", controller.RelayTask)
	}
}
