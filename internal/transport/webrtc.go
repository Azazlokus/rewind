package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// Реализация Conn поверх WebRTC DataChannel (итерация 11; два канала — итерация 12).
//
// Игровой код по-прежнему видит только Conn — как и с WebSocket. Транспортная
// абстракция строилась именно под это: pion не протекает выше пакета transport,
// симуляция и кодек ничего про WebRTC не знают.
//
// Два DataChannel (итерация 12). У сообщений разная ценность, поэтому и каналов два:
//   - "game"  — ordered+reliable (дефолт SCTP): JoinAck, Spawn/Death/Hit, вводы.
//     Семантика WebSocket, ничего терять нельзя.
//   - "state" — unordered+unreliable (MaxRetransmits=0): снапшоты. Потерянный
//     снапшот бесполезен, а на надёжном канале он бы ещё и держал head-of-line
//     blocking для следующих. Раздельный канал убирает и это, и блокировку
//     reliable-событий за снапшотами.
// Оба канала создаёт offerer (клиент/DialWebRTC), answerer (сервер/AcceptWebRTC)
// принимает их через OnDataChannel. Направление данных: "game" двунаправлен,
// "state" — только сервер→клиент. Reliable-путь наружу — Write, unreliable —
// WriteUnreliable (UnreliableWriter); снапшоты роутит на него Session.
//
// Модель конкурентности. Каждый DataChannel доставляет входящие сообщения колбэком
// OnMessage с горутины pion; мост в блокирующий Read — общий буферизованный канал
// recv (с backpressure: полный буфер тормозит колбэк, а не теряет сообщение). Оба
// канала кладут в один recv — читатель декодирует по типу сообщения и не знает, с
// какого канала пришло. Close закрывает done (будит и Read, и заблокированный
// колбэк) и затем PeerConnection. Одна пара читатель/писатель, как требует Conn.

// каналы данных: метки фиксированы и должны совпадать с web/game.js.
const (
	channelReliable   = "game"  // ordered+reliable
	channelUnreliable = "state" // unordered+unreliable (снапшоты)
)

// ICEServer описывает один STUN/TURN-сервер. Поля повторяют и pion webrtc.ICEServer,
// и браузерный RTCIceServer (urls/username/credential), поэтому список без изменений
// сериализуется в сигналинг и отдаётся клиенту. Username/Credential нужны TURN
// (статические креды); для STUN они пусты.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// WebRTCConfig конфигурирует WebRTC-транспорт (сервер и клиент).
type WebRTCConfig struct {
	// ICEServers — STUN/TURN-серверы. Пусто — только host-кандидаты (localhost/LAN,
	// наружу никто не ходит). TURN несёт креды (Username/Credential) для обхода NAT.
	ICEServers []ICEServer
	// ForceRelay включает ICETransportPolicy=relay: соединение только через TURN
	// (host/srflx-кандидаты отбрасываются). Нужно в жёстких сетях/за симметричным NAT
	// и для приватности (реальный IP пира не светится). Задаёт сервер; клиенту
	// политика уезжает в config-сигналинге, чтобы обе стороны совпали.
	ForceRelay bool
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
	Kind       string      `json:"kind"`                 // "config" | "offer" | "answer"
	SDP        string      `json:"sdp,omitempty"`        // для offer/answer
	ICEServers []ICEServer `json:"iceServers,omitempty"` // для config (сервер → клиент)
	ForceRelay bool        `json:"forceRelay,omitempty"` // для config: политика relay-only
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
	if err := writeSignal(ctx, signaling, signalMsg{Kind: "config", ICEServers: cfg.ICEServers, ForceRelay: cfg.ForceRelay}); err != nil {
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

	rc, err := newRTCConn(cfg.ICEServers, cfg.ForceRelay, signaling.RemoteAddr())
	if err != nil {
		return nil, err
	}
	pc := rc.pc

	// Приёмник DataChannel готовим до setRemoteDescription, иначе можно пропустить
	// канал; OnMessage вешается сразу в bindChannel — ранние сообщения буферизуются.
	// Клиент открывает оба канала ("game"+"state"); bindChannel роутит по метке и
	// чужие метки игнорирует.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
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

	// ICE-серверы и политику relay диктует сервер (config), как и браузерному клиенту:
	// так обе стороны собирают согласованных кандидатов.
	rc, err := newRTCConn(config.ICEServers, config.ForceRelay, signaling.RemoteAddr())
	if err != nil {
		return nil, err
	}
	pc := rc.pc

	// Клиент — offerer, он создаёт оба канала. "game" ordered+reliable (дефолт),
	// "state" unordered+unreliable под снапшоты.
	reliable, err := pc.CreateDataChannel(channelReliable, nil)
	if err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc create data channel: %w", err)
	}
	rc.bindChannel(reliable)
	unreliable, err := pc.CreateDataChannel(channelUnreliable, &webrtc.DataChannelInit{
		Ordered:        boolPtr(false),
		MaxRetransmits: u16Ptr(0),
	})
	if err != nil {
		rc.shutdown()
		return nil, fmt.Errorf("transport: webrtc create unreliable data channel: %w", err)
	}
	rc.bindChannel(unreliable)

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

// iceServers переводит список в конфиг pion. Пустой список — без ICE-серверов
// (только host-кандидаты). TURN-креды (Username/Credential) пробрасываются как есть.
func iceServers(servers []ICEServer) []webrtc.ICEServer {
	if len(servers) == 0 {
		return nil
	}
	out := make([]webrtc.ICEServer, len(servers))
	for i, s := range servers {
		out[i] = webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential}
	}
	return out
}

// boolPtr/u16Ptr — pion принимает опции DataChannel по указателю.
func boolPtr(b bool) *bool    { return &b }
func u16Ptr(v uint16) *uint16 { return &v }

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

// rtcConn — Conn поверх двух DataChannel (reliable "game" + unreliable "state").
type rtcConn struct {
	pc   *webrtc.PeerConnection
	recv chan []byte
	done chan struct{}
	addr string

	// mu защищает поля каналов при установке и проверку «оба открыты». Только
	// setup/колбэки открытия — не горячий путь. После awaitOpen поля не меняются, и
	// Read/Write читают их без блокировки (happens-before через close(opened)).
	mu           sync.Mutex
	dcReliable   *webrtc.DataChannel // "game": JoinAck, события, вводы
	dcUnreliable *webrtc.DataChannel // "state": снапшоты (best-effort)

	opened    chan struct{} // закрыт, когда оба канала открыты
	openOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

// newRTCConn создаёт PeerConnection и обёртку. Разрыв соединения (Failed/Closed/
// Disconnected) будит висящие Read/Write через done.
func newRTCConn(servers []ICEServer, forceRelay bool, addr string) (*rtcConn, error) {
	pcCfg := webrtc.Configuration{ICEServers: iceServers(servers)}
	if forceRelay {
		pcCfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	pc, err := webrtc.NewPeerConnection(pcCfg)
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

// bindChannel роутит DataChannel по метке в reliable/unreliable, вешает мост в recv
// и сигналы открытия/закрытия. OnMessage ставится сразу, поэтому ранние сообщения
// (Join приходит на onopen "game") буферизуются. Чужие метки игнорируются.
func (r *rtcConn) bindChannel(dc *webrtc.DataChannel) {
	r.mu.Lock()
	switch dc.Label() {
	case channelReliable:
		r.dcReliable = dc
	case channelUnreliable:
		r.dcUnreliable = dc
	default:
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// Копируем: pion переиспользует буфер после возврата колбэка. Оба канала
		// кладут в один recv — читатель различает сообщения по типу, не по каналу.
		b := make([]byte, len(msg.Data))
		copy(b, msg.Data)
		select {
		case r.recv <- b:
		case <-r.done:
		}
	})
	dc.OnOpen(func() { r.onChannelOpen() })
	dc.OnClose(func() { r.shutdown() })
}

// onChannelOpen закрывает opened, когда открыты ОБА канала. Колбэки открытия двух
// каналов приходят независимо с горутин pion; тот, что открылся последним, увидит
// оба готовыми. mu синхронизирует чтение полей с их установкой в bindChannel.
func (r *rtcConn) onChannelOpen() {
	r.mu.Lock()
	ready := r.dcReliable != nil && r.dcReliable.ReadyState() == webrtc.DataChannelStateOpen &&
		r.dcUnreliable != nil && r.dcUnreliable.ReadyState() == webrtc.DataChannelStateOpen
	r.mu.Unlock()
	if ready {
		r.openOnce.Do(func() { close(r.opened) })
	}
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

// awaitOpen ждёт открытия ОБОИХ каналов (или таймаута/разрыва) и возвращает Conn.
// Ждём оба, чтобы первый же WriteUnreliable (снапшот) не ушёл в ещё не открытый
// канал.
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

// Write — reliable-путь ("game"): JoinAck, события, вводы.
func (r *rtcConn) Write(ctx context.Context, msg []byte) error {
	return r.send(r.dcReliable, msg)
}

// WriteUnreliable — best-effort путь ("state"): снапшоты. Реализует UnreliableWriter;
// Session роутит сюда дропаемые снапшоты, остальное идёт надёжным Write.
func (r *rtcConn) WriteUnreliable(ctx context.Context, msg []byte) error {
	return r.send(r.dcUnreliable, msg)
}

func (r *rtcConn) send(dc *webrtc.DataChannel, msg []byte) error {
	select {
	case <-r.done:
		return fmt.Errorf("transport: webrtc write: %w", ErrClosed)
	default:
	}
	// dc.Send копирует данные в SCTP-буфер, поэтому msg можно переиспользовать сразу
	// после возврата (контракт Conn.Write). Поля каналов после awaitOpen неизменны.
	if err := dc.Send(msg); err != nil {
		return fmt.Errorf("transport: webrtc write: %w", err)
	}
	return nil
}

func (r *rtcConn) Close(reason string) error {
	r.shutdown()
	return r.closeErr
}

func (r *rtcConn) RemoteAddr() string { return r.addr }

// проверки на этапе компиляции: rtcConn — это Conn и умеет best-effort доставку.
var (
	_ Conn             = (*rtcConn)(nil)
	_ UnreliableWriter = (*rtcConn)(nil)
)
