package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Images handles OpenAI Images API requests.
// Supported endpoints:
//
//	POST /v1/images/generations - 图像生成
//	POST /v1/images/edits - 图像编辑
//
// 主要处理流程：
// 1. 认证与授权检查
// 2. 请求解析（支持 JSON 和 multipart/form-data）
// 3. 权限与内容审核
// 4. 并发控制（图像生成槽位、用户槽位、账户槽位）
// 5. 计费检查
// 6. 账户选择与故障转移
// 7. 请求转发到上游
// 8. 使用记录
func (h *OpenAIGatewayHandler) Images(c *gin.Context) {
	// 标记流式响应是否已开始，用于 panic 恢复时判断是否需要返回错误响应
	streamStarted := false
	// 注册 panic 恢复机制，防止异常导致服务崩溃
	defer h.recoverResponsesPanic(c, &streamStarted)

	// 记录请求开始时间，用于统计各阶段耗时
	requestStart := time.Now()

	// ========== 阶段1：认证检查 ==========
	// 从上下文获取 API Key
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	// 从上下文获取用户认证信息
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}

	// 创建请求日志记录器，包含用户和 API Key 信息
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.images",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	// 检查必要的依赖服务是否可用
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	// ========== 阶段2：请求体读取 ==========
	// 读取请求体（预分配内存优化性能）
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	// 设置操作日志上下文（先记录空模型，后续更新）
	if isMultipartImagesContentType(c.GetHeader("Content-Type")) {
		setOpsRequestContext(c, "", false)
	} else {
		setOpsRequestContext(c, "", false)
	}

	// ========== 阶段3：请求解析 ==========
	// 解析请求体，支持 JSON 和 multipart 两种格式
	parsed, err := h.gatewayService.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	requestModel := parsed.Model
	ensureCompositeTargetPlatform(c, apiKey, requestModel)
	clientRequestModel := clientRequestedModel(c, requestModel)
	routingModel := requestModel
	if resolvedModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok {
		routingModel = resolvedModel
	}
	if !compositeTargetPlatformAllowed(c, apiKey, requestModel, service.PlatformOpenAI) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}

	// 更新日志记录器，添加解析后的请求信息
	reqLog = reqLog.With(
		zap.String("model", clientRequestModel),
		zap.String("routing_model", routingModel),
		zap.Bool("stream", parsed.Stream),
		zap.Bool("multipart", parsed.Multipart),
		zap.String("capability", string(parsed.RequiredCapability)),
		zap.String("img_quality", parsed.Quality),
		zap.String("img_size", parsed.Size),
	)

	// ========== 阶段4：权限与内容审核 ==========
	// 检查分组是否允许图像生成
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}
	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIImages, requestModel, parsed.ModerationBody()); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}

	// ========== 阶段5：并发控制 ==========
	// 获取图像生成槽位（限制同时进行的图像生成请求数）
	imageReleaseFunc, acquired := h.acquireImageGenerationSlot(c, streamStarted)
	if !acquired {
		return
	}
	if imageReleaseFunc != nil {
		defer imageReleaseFunc()
	}

	setOpsRequestContext(c, clientRequestModel, parsed.Stream)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(parsed.Stream, false)))

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, routingModel)

	// 绑定错误透传服务（如果配置了）
	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	// 获取订阅信息（用于计费检查）
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// 记录认证阶段耗时
	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	// 获取用户并发槽位（限制单个用户的并发请求数）
	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, parsed.Stream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// ========== 阶段6：计费检查 ==========
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai.images.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	// 生成会话哈希（用于会话粘性和账户选择）
	sessionHash := h.gatewayService.GenerateExplicitSessionHash(c, body)
	requestCtx := service.WithOpenAIImagesEndpoint(service.WithOpenAIImageGenerationIntent(c.Request.Context()))

	// ========== 阶段7：账户选择与故障转移循环 ==========
	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	profitVetoCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	stopJSONKeepalive := func() {}
	jsonKeepaliveStarted := false
	defer func() { stopJSONKeepalive() }()
	var oauth429FailoverState service.OpenAIOAuth429FailoverState

	for {
		reqLog.Debug("openai.images.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))

		// 使用调度器选择合适的账户
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForImages(
			requestCtx,
			apiKey.GroupID,
			sessionHash,
			routingModel,
			failedAccountIDs,
			parsed.RequiredCapability,
		)

		// 账户选择失败处理
		if err != nil {
			if failoverClientGone(c) {
				reqLog.Info("openai.images.account_select_aborted_client_disconnected", zap.Error(err))
				return
			}
			reqLog.Warn("openai.images.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if len(failedAccountIDs) == 0 {
				cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey, clientRequestModel, routingModel, service.PlatformOpenAI)
				if !cls.ModelNotFound {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				}
				message := cls.Message
				if !cls.ModelNotFound {
					message = "No available compatible accounts"
				}
				h.handleStreamingAwareError(c, cls.Status, cls.ErrType, message, streamStarted)
				return
			}
			// 所有可用账户都已尝试过，返回失败
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
			} else {
				h.handleFailoverExhaustedSimple(c, 502, streamStarted)
			}
			return
		}

		if selection == nil || selection.Account == nil {
			cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey, clientRequestModel, routingModel, service.PlatformOpenAI)
			if !cls.ModelNotFound {
				markOpsRoutingCapacityLimited(c)
			}
			message := cls.Message
			if !cls.ModelNotFound {
				message = "No available compatible accounts"
			}
			h.handleStreamingAwareError(c, cls.Status, cls.ErrType, message, streamStarted)
			return
		}

		// 记录调度决策信息
		reqLog.Debug("openai.images.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)

		account := selection.Account
		// 更新会话哈希（池模式下确保会话粘性）
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai.images.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, slotResult := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, parsed.Stream, &streamStarted, reqLog)
		if slotResult == openAISlotAcquireProfitVetoed {
			// Images 调度不装利润门，此分支实际不可达；防御性排除重选并受同一否决上限约束。
			if !recordOpenAIProfitVeto(failedAccountIDs, account.ID, &profitVetoCount) {
				h.handleOpenAIProfitVetoExhausted(c, streamStarted, reqLog, profitVetoCount)
				return
			}
			continue
		}
		if slotResult != openAISlotAcquireOK {
			return
		}

		// ========== 阶段8：请求转发 ==========
		// 记录路由阶段耗时
		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		if !parsed.Stream && !jsonKeepaliveStarted {
			stopJSONKeepalive = service.StartOpenAIImagesJSONKeepalive(c, h.openAIImagesJSONKeepaliveInterval())
			jsonKeepaliveStarted = true
		}
		forwardStart := time.Now()
		writerSizeBeforeForward := service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			return h.gatewayService.ForwardImages(requestCtx, c, account, body, parsed, channelMapping.MappedModel)
		}()
		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}

		// ========== 阶段9：错误处理与故障转移 ==========
		if err != nil {
			// 部分成功情况：已有图像返回但发生错误
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai.images.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
			} else {
				// 故障转移处理
				var imageUpstreamErr *service.OpenAIImagesUpstreamError
				if errors.As(err, &imageUpstreamErr) {
					retryableServerError := service.IsOpenAIImagesRetryableUpstreamError(imageUpstreamErr)
					if retryableServerError {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), false, nil, err)
					} else {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), true, nil)
					}
					logEvent := "openai.images.upstream_user_error"
					if retryableServerError {
						logEvent = "openai.images.upstream_server_error_after_flush"
					}
					reqLog.Warn(logEvent,
						zap.Int64("account_id", account.ID),
						zap.Int("status_code", imageUpstreamErr.StatusCode),
						zap.String("error_type", imageUpstreamErr.ErrorType),
						zap.String("error_code", imageUpstreamErr.Code),
						zap.Error(err),
					)
					return
				}
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), false, nil, err)
					if service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) != writerSizeBeforeForward {
						reqLog.Warn("openai.images.upstream_failover_skipped_after_flush",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
						)
						h.handleFailoverExhausted(c, failoverErr, true)
						return
					}
					if failoverClientGone(c) {
						reqLog.Info("openai.images.failover_aborted_client_disconnected",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
						)
						return
					}

					// 池模式下同一账户重试
					if failoverErr.RetryableOnSameAccount {
						retryLimit := effectiveSameAccountRetryLimit(failoverErr, account)
						if sameAccountRetryAllowed(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
							sameAccountRetryCount[account.ID]++
							retryDelay := sameAccountRetryDelayFor(failoverErr, sameAccountRetryCount[account.ID])
							reqLog.Warn("openai.images.pool_mode_same_account_retry",
								zap.Int64("account_id", account.ID),
								zap.Int("upstream_status", failoverErr.StatusCode),
								zap.Int("retry_limit", retryLimit),
								zap.Int("retry_count", sameAccountRetryCount[account.ID]),
								zap.Duration("retry_delay", retryDelay),
							)
							// 等待后重试同一账户
							select {
							case <-requestCtx.Done():
								return
							case <-time.After(retryDelay):
							}
							continue
						}
					}

					// 切换到下一个账户
					h.gatewayService.RecordOpenAIAccountSwitch()
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = failoverErr

					// 检查是否已达到最大切换次数
					if switchCount >= maxAccountSwitches {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					switchCount++
					if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount, &oauth429FailoverState) {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					reqLog.Warn("openai.images.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", maxAccountSwitches),
					)
					continue
				}
				if shouldFailoverOpenAIImagesForwardError(err) && c.Request.Context().Err() == nil {
					h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), false, nil, err)
					h.gatewayService.RecordOpenAIAccountSwitch()
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
					if switchCount >= maxAccountSwitches {
						h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
						return
					}
					switchCount++
					reqLog.Warn("openai.images.forward_error_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", maxAccountSwitches),
						zap.Error(err),
					)
					continue
				}

				// 非故障转移错误，直接返回
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), false, nil, err)
				upstreamErrorAlreadyCommunicated := openAIForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
				wroteFallback := false
				if !upstreamErrorAlreadyCommunicated {
					wroteFallback = h.ensureForwardErrorResponse(c, streamStarted)
				}
				fields := []zap.Field{
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
					zap.Error(err),
				}
				if shouldLogOpenAIForwardFailureAsWarn(c, wroteFallback) {
					reqLog.Warn("openai.images.forward_failed", fields...)
					return
				}
				reqLog.Error("openai.images.forward_failed", fields...)
				return
			}
		}

		// ========== 阶段10：成功处理 ==========
		if result != nil {
			// 排除 spark 影子:其 codex_* 仅由 QueryUsage(/wham/usage bengalfox)更新(外审第7轮 P1)。
			if account.Type == service.AccountTypeOAuth && !account.IsShadow() {
				h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), account.ID, result.ResponseHeaders)
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), true, result.FirstTokenMs)
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, openAIAccountScheduleModel(c, account, requestModel, false, result), true, nil)
		}

		// ========== 阶段11：使用记录 ==========
		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		requestPayloadHash := service.HashUsageRequestPayload(body)
		if parsed.Multipart {
			requestPayloadHash = service.HashUsageRequestPayload([]byte(parsed.StickySessionSeed()))
		}
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)

		upstreamModel := ""
		if result != nil {
			upstreamModel = result.UpstreamModel
		}
		sessionID := service.ExtractClientSessionID(c)

		// 异步提交使用记录任务
		h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				RequestPayloadHash: requestPayloadHash,
				APIKeyService:      h.apiKeyService,
				QuotaPlatform:      quotaPlatform,
				SessionID:          sessionID,
				ChannelUsageFields: clientRequestedUsageFields(c, channelMapping, requestModel, upstreamModel),
			}); err != nil {
				logger.L().With(
					zap.String("component", "handler.openai_gateway.images"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", clientRequestModel),
					zap.Int64("account_id", account.ID),
				).Error("openai.images.record_usage_failed", zap.Error(err))
			}
		})

		reqLog.Debug("openai.images.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func shouldFailoverOpenAIImagesForwardError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "upstream request failed")
}

func (h *OpenAIGatewayHandler) openAIImagesJSONKeepaliveInterval() time.Duration {
	if h.cfg == nil || h.cfg.Gateway.ImageNonstreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(h.cfg.Gateway.ImageNonstreamKeepaliveInterval) * time.Second
}

func isMultipartImagesContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data")
}
