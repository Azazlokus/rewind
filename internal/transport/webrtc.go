package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// Реализация Conn поверх WebRTC DataChannel (итерация 11).
//
// Игровой код по-прежнему видит только Conn — как и с WebSocket. Транспортная
// абстракция строилась именно под это: pion не протекает выше пакета transport,
// симуляция и кодек ничего про WebRTC не знают.
//
// Модель конкурентности. DataChannel доставляет входящие сообщения колбэком
// OnMessage с горутины pion; мост в блокирующий Read — буферизованный канал recv,
// в который колбэк кладёт данные (с backpressure: полный буфер тормозит колбэк,
// а не теряет reliable-сообщение). Close закрывает done (будит и Read, и
// заблокированный колбэк) и затем PeerConnection. Одна пара читатель/писатель,
// как требует контракт Conn.

// WebRTCConfig конфигурирует WebRTC-транспорт (сервер и клиент).
type WebRTCConfig struct {
	// ICEServers — список STUN/TURN URL (например, "stun:stun.l.google.com:19302").
	// Пусто — только host-кандидаты (localhost/LAN, наружу никто не ходит).
	ICEServers []string
	// ConnectTimeout ограничивает рукопожатие до открытия DataChannel. Ноль — 15 с.
	ConnectTimeout time.Duration
}

func (c WebRTCConfig) withDefaults() WebRTCConfig {
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 15 * time.Second
	}
	return c
}

// recvBuffer — глубина буфера входящих сообщений DataChannel. С запасом на пачку
// вводов между двумя Read; при переполнении колбэк притормаживается (backpressure),
// а не теряет сообщение.
const recvBuffer = 64

// signalMsg — одно сигналинг-сообщение поверх WS. Сигналинг — не горячий путь,
// поэтому JSON здесь разрешён (в отличие от игрового протокола).
type signalMsg struct {
	Kind       string   `json:"kind"`                 // "config" | "offer" | "answer"
	SDP        string   `json:"sdp,omitempty"`        // для offer/answer
	ICEServers []string `json:"iceServers,omitempty"` // для config (сервер → клиент)
}

// AcceptWebRTC (серверная сторона) проводит рукопожатие WebRTC поверх уже
// установленного сигналинг-соединения (обычно WebSocket) и возвращает игровой Conn
// поверх открытого DataChannel. Сигналинг: сервер шлёт config (ICE-серверы), читает
// offer клиента, отвечает answer. ICE — non-trickle: обе стороны собирают кандидатов
// до отправки SDP (для localhost мгновенно), поэтому отдельных candidate-сообщений и
// фоновой горутины сигналинга нет. При ошибке PeerConnection закрывается, чтобы не течь.
func AcceptWebRTC(ctx context.Context, signaling Conn, cfg WebRTCConfig) (Conn, error) {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	// 1. Сообщаем клиенту ICE-серверы, чтобы обе стороны собирали одинаковых
	//    кандидатов (для localhost список пуст — только host).
	if err := writeSignal(ctx, signaling, signalMsg{Kind: "config", ICEServers: cfg.ICEServers}); err != nil {
		return nil, fmt.Errorf("transport: webrtc send config: %w", err)
	}

	// 2. Читаем offer клиента (он — offerer, он же создаёт DataChannel "game").
	offer, err := readSignal(ctx, signaling)
	if err != nil {
		return nil, fmt.Errorf("transport: webrtc read offer: %w", err)
	}
	if offer.Kind != "offer" {
		return nil, fmt.Errorf("transport: webrtc expected offer, got %q", offer.Kind)
	}

	rc, err := newRTCConn(cfg.ICEServers, signaling.RemoteAddr())
	if err != nil {
		return nil, err
	}
	pc := rc.pc

	// Приёмник DataChannel готовим до setRemoteDescription, иначе можно пропустить
	// канал; OnMessage вешается сразу в bindChannel — ранние сообщения буферизуются.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "game" {
			return // чужой канал игнорируем
		}
		rc.bindChannel(dc)
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc set remote description: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc create answer: %w", err)
	}
	if err := rc.completeLocal(ctx, answer); err != nil {
		return nil, err
	}
	if err := writeSignal(ctx, signaling, signalMsg{Kind: "answer", SDP: pc.LocalDescription().SDP}); err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc send answer: %w", err)
	}
	return rc.awaitOpen(ctx)
}

// DialWebRTC (клиентская сторона) — offerer: читает config, создаёт DataChannel
// "game", шлёт offer, принимает answer, ждёт открытия канала. Зеркалит логику
// web/game.js; используется в тестах и как основа возможного WebRTC-бота. Игровой
// Conn поверх DataChannel — тот же rtcConn, что и на сервере.
func DialWebRTC(ctx context.Context, signaling Conn, cfg WebRTCConfig) (Conn, error) {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	config, err := readSignal(ctx, signaling)
	if err != nil {
		return nil, fmt.Errorf("transport: webrtc read config: %w", err)
	}
	if config.Kind != "config" {
		return nil, fmt.Errorf("transport: webrtc expected config, got %q", config.Kind)
	}

	// ICE-серверы диктует сервер (config), как и браузерный клиент.
	rc, err := newRTCConn(config.ICEServers, signaling.RemoteAddr())
	if err != nil {
		return nil, err
	}
	pc := rc.pc

	dc, err := pc.CreateDataChannel("game", nil) // ordered+reliable по умолчанию
	if err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc create data channel: %w", err)
	}
	rc.bindChannel(dc)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc create offer: %w", err)
	}
	if err := rc.completeLocal(ctx, offer); err != nil {
		return nil, err
	}
	if err := writeSignal(ctx, signaling, signalMsg{Kind: "offer", SDP: pc.LocalDescription().SDP}); err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc send offer: %w", err)
	}
	answer, err := readSignal(ctx, signaling)
	if err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc read answer: %w", err)
	}
	if answer.Kind != "answer" {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc expected answer, got %q", answer.Kind)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc set remote description: %w", err)
	}
	return rc.awaitOpen(ctx)
}

// iceServers переводит список URL в конфиг pion. Пустой список — без ICE-серверов
// (только host-кандидаты).
func iceServers(urls []string) []webrtc.ICEServer {
	if len(urls) == 0 {
		return nil
	}
	return []webrtc.ICEServer{{URLs: urls}}
}

func writeSignal(ctx context.Context, c Conn, m signalMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.Write(ctx, b)
}

func readSignal(ctx context.Context, c Conn) (signalMsg, error) {
	data, err := c.Read(ctx)
	if err != nil {
		return signalMsg{}, err
	}
	var m signalMsg
	if err := json.Unmarshal(data, &m); err != nil {
		return signalMsg{}, fmt.Errorf("transport: webrtc bad signal: %w", err)
	}
	return m, nil
}

// rtcConn — Conn поверх открытого DataChannel.
type rtcConn struct {
	pc   *webrtc.PeerConnection
	dc   *webrtc.DataChannel
	recv chan []byte
	done chan struct{}
	addr string

	opened    chan struct{}
	openOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

// newRTCConn создаёт PeerConnection и обёртку. Разрыв соединения (Failed/Closed/
// Disconnected) будит висящие Read/Write через done.
func newRTCConn(iceURLs []string, addr string) (*rtcConn, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers(iceURLs)})
	if err != nil {
		return nil, fmt.Errorf("transport: webrtc new peer connection: %w", err)
	}
	rc := &rtcConn{
		pc:     pc,
		recv:   make(chan []byte, recvBuffer),
		done:   make(chan struct{}),
		opened: make(chan struct{}),
		addr:   addr,
	}
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed || s == webrtc.PeerConnectionStateDisconnected {
			rc.shutdown()
		}
	})
	return rc, nil
}

// bindChannel вешает на DataChannel мост в recv и сигнал открытия/закрытия. OnMessage
// ставится сразу, поэтому ранние сообщения (Join приходит на onopen) буферизуются.
func (r *rtcConn) bindChannel(dc *webrtc.DataChannel) {
	r.dc = dc
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// Копируем: pion переиспользует буфер после возврата колбэка.
		b := make([]byte, len(msg.Data))
		copy(b, msg.Data)
		select {
		case r.recv <- b:
		case <-r.done:
		}
	})
	dc.OnOpen(func() { r.openOnce.Do(func() { close(r.opened) }) })
	dc.OnClose(func() { r.shutdown() })
}

// completeLocal ставит локальное описание и ждёт сбора всех ICE-кандидатов
// (non-trickle) — тогда LocalDescription() несёт полный SDP с кандидатами.
func (r *rtcConn) completeLocal(ctx context.Context, desc webrtc.SessionDescription) error {
	gatherDone := webrtc.GatheringCompletePromise(r.pc)
	if err := r.pc.SetLocalDescription(desc); err != nil {
		r.shutdown()
		return fmt.Errorf("transport: webrtc set local description: %w", err)
	}
	select {
	case <-gatherDone:
		return nil
	case <-ctx.Done():
		r.shutdown()
		return fmt.Errorf("transport: webrtc gather ice: %w", ctx.Err())
	}
}

// awaitOpen ждёт открытия DataChannel (или таймаута/разрыва) и возвращает Conn.
func (r *rtcConn) awaitOpen(ctx context.Context) (Conn, error) {
	select {
	case <-r.opened:
		return r, nil
	case <-r.done:
		return nil, fmt.Errorf("transport: webrtc peer connection lost before open: %w", ErrClosed)
	case <-ctx.Done():
		r.shutdown()
		return nil, fmt.Errorf("transport: webrtc data channel open: %w", ctx.Err())
	}
}

// shutdown закрывает done и PeerConnection ровно один раз. Через него проходят и
// внешний Close, и внутренние сигналы разрыва (OnClose/OnConnectionStateChange).
// Порядок важен: сначала done (будит заблокированный на recv колбэк и Read), потом
// pc.Close() (останавливает read loop канала).
func (r *rtcConn) shutdown() {
	r.closeOnce.Do(func() {
		close(r.done)
		if err := r.pc.Close(); err != nil {
			r.closeErr = fmt.Errorf("transport: webrtc close: %w", err)
		}
	})
}

func (r *rtcConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-r.recv:
		return b, nil
	case <-r.done:
		return nil, fmt.Errorf("transport: webrtc read: %w", ErrClosed)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *rtcConn) Write(ctx context.Context, msg []byte) error {
	select {
	case <-r.done:
		return fmt.Errorf("transport: webrtc write: %w", ErrClosed)
	default:
	}
	// dc.Send копирует данные в SCTP-буфер, поэтому msg можно переиспользовать сразу
	// после возврата (контракт Conn.Write).
	if err := r.dc.Send(msg); err != nil {
		return fmt.Errorf("transport: webrtc write: %w", err)
	}
	return nil
}

func (r *rtcConn) Close(reason string) error {
	r.shutdown()
	return r.closeErr
}

func (r *rtcConn) RemoteAddr() string { return r.addr }

// проверка на этапе компиляции, что rtcConn удовлетворяет Conn.
var _ Conn = (*rtcConn)(nil)
