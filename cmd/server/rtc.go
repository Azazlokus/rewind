package main

import (
	"log/slog"
	"net/http"
	"time"

	"arena/internal/transport"
)

// rtcGateway обслуживает WebRTC-транспорт (итерация 11). Апгрейдит запрос до
// WebSocket-канала СИГНАЛИНГА (текстовый JSON: config/offer/answer), проводит по
// нему рукопожатие WebRTC и дальше отдаёт готовый DataChannel-Conn общему
// gateway.serve — та же сессия, что и у WebSocket, транспорт ниже не важен.
//
// Транспорт выбирается клиентом явно (web/game.js ?transport=webrtc), фолбэка нет:
// путь /ws остаётся дефолтным и обслуживает ботов и e2e без изменений.
type rtcGateway struct {
	base    *gateway
	log     *slog.Logger
	sigOpts transport.WSOptions
	rtc     transport.WebRTCConfig
}

func newRTCGateway(base *gateway, log *slog.Logger, cfg serverConfig) *rtcGateway {
	return &rtcGateway{
		base: base,
		log:  log,
		sigOpts: transport.WSOptions{
			// Сигналинг — JSON-текст (не горячий путь), в отличие от бинарной игры.
			WriteKind:          transport.KindText,
			ReadLimit:          64 << 10, // SDP с ICE-кандидатами бывает крупным
			WriteTimeout:       5 * time.Second,
			InsecureSkipVerify: base.wsOpts.InsecureSkipVerify,
		},
		rtc: transport.WebRTCConfig{ICEServers: cfg.ICEServers, ForceRelay: cfg.ForceRelay},
	}
}

// ServeHTTP апгрейдит сигналинг-WS, проводит WebRTC-рукопожатие и обслуживает
// сессию поверх DataChannel. Сигналинг-WS живёт только на время рукопожатия
// (non-trickle: после answer он не нужен) и закрывается до старта игры.
func (g *rtcGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sig, err := transport.Upgrade(w, r, g.sigOpts)
	if err != nil {
		g.log.Debug("rtc signaling upgrade failed", "addr", r.RemoteAddr, "err", err)
		return
	}

	// Рукопожатие ограничиваем базовым контекстом сервера, а не контекстом запроса:
	// AcceptWebRTC сам ставит внутренний таймаут (WebRTCConfig.ConnectTimeout).
	conn, err := transport.AcceptWebRTC(r.Context(), sig, g.rtc)
	// Сигналинг больше не нужен (non-trickle): закрываем независимо от исхода.
	_ = sig.Close("signaling done")
	if err != nil {
		g.log.Debug("rtc handshake failed", "addr", r.RemoteAddr, "err", err)
		return
	}
	g.log.Debug("rtc data channel open", "addr", r.RemoteAddr)

	g.base.serve(r, conn)
}

// проверка на этапе компиляции, что rtcGateway удовлетворяет http.Handler.
var _ http.Handler = (*rtcGateway)(nil)
