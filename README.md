# bizgo-sdk-comm-go

[비즈고(Bizgo)](https://bizgo.io) 커뮤니케이션 API의 Go SDK입니다.
SMS/LMS/MMS, 국제문자, RCS, 카카오 알림톡·브랜드메시지·상담톡, 네이버 톡톡을 하나의 클라이언트로 발송하고, 예약 발송·템플릿·발신프로필·인사이트까지 **API 전체(146개 operation, 웹훅 9종)** 를 다룹니다.

- Go 1.26 이상 · **서드파티 런타임 의존성 없음**(표준 라이브러리만) · 모든 호출은 `context.Context`를 받고, 클라이언트는 동시 사용에 안전합니다
- 요청은 보내기 전에 검증합니다: 필수 필드, 조건부 필수(`x-sdk-required-if`, 예: 알림톡 전문 발송의 `msgType`), 길이, **바이트 길이**(SMS 90byte 등, EUC-KR 기준), EUC-KR 범위 밖 문자, 수신자 200명, 허용값, 형식
- 편의 기능: 대량 발송(`Send.Bulk`), 클라이언트 속도 제한, 테스트 도구(`bizgotest`), 관측 hooks와 OpenTelemetry 어댑터(별도 모듈)
- 스펙: [bizgo-api-spec](https://github.com/icomm-api/bizgo-api-spec) (OpenAPI 3.1) · 원문: [API 레퍼런스](https://developers.bizgo.io/api-sdk/api-reference)

> 이 모듈은 `infobank-omni-sdk-go`(OMNI API, ID/PW 인증, 더 이상 유지보수되지 않음)의 후속입니다.
> 옮기는 방법은 [이전 SDK에서 옮기기](#이전-sdk에서-옮기기)를 참고하세요.

## 설치

```bash
go get github.com/icomm-api/bizgo-sdk-comm-go@v1.2.0   # 최신 버전은 @latest
```

```go
import "github.com/icomm-api/bizgo-sdk-comm-go" // 패키지 이름: bizgo
```

## 시작하기

1. 콘솔 `발송관리 > 연동관리`에서 **API Key**를 발급하고, 호출할 서버의 **공인 IP를 등록**합니다.
2. 키를 환경변수로 설정합니다. 키는 코드나 저장소에 쓰지 않습니다.

```bash
export BIZGO_API_KEY=...
```

3. sandbox(실제 발송 없음)에서 먼저 확인합니다.

```go
client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox)) // 키는 BIZGO_API_KEY에서 읽음
if err != nil {
	log.Fatal(err)
}
result, err := client.Send.SMS(ctx, bizgo.SMSParams{
	To:   bizgo.To("01000000000"),
	From: "01000000000",
	Text: "[비즈고] 인증번호는 123456 입니다.",
})
if err != nil {
	log.Fatal(err)
}
fmt.Println(result.MsgKeys()) // 접수된 메시지 키
fmt.Println(result.Failed())  // 접수 단계에서 거절된 수신자 (없으면 빈 목록, 전화번호는 010****0000으로 표시)
fmt.Println(result.Duplicates()) // 같은 IdempotencyKey로 이미 접수된 수신자 (A301, 다시 발송되지 않음)
```

운영에 보낼 때는 `WithEnvironment`를 생략하거나 `bizgo.Production`을 씁니다.

| 옵션 | 기본값 |
|---|---|
| `WithAPIKey(key)` | 환경변수 `BIZGO_API_KEY` (공백·줄바꿈이 있으면 설정 오류, 잘라내지 않음) |
| `WithEnvironment(bizgo.Sandbox)` | `bizgo.Production` (`https://mars.ibapi.kr`) |
| `WithBaseURL(url)` | https만 허용(테스트용 localhost 제외). 사용자 정보·쿼리·fragment 불가 |
| `WithTimeout(d)` | 시도당 30초 (연결 5초) |
| `WithMaxRetries(n)` | 2 |
| `WithRateLimit(send, other)` / `WithoutRateLimit()` | 켜짐: 발송 초당 200 **메시지**, 그 외 초당 5 요청 ([속도 제한](#속도-제한)) |
| `WithProxy(url)` | 없음(환경변수 `HTTPS_PROXY` 등). `http://`·`https://`·`socks5://` 프록시 |
| `WithRootCAs(pool)` / `WithRootCAsFile(pem)` | 시스템 인증서 저장소. 사내 CA로 서버 인증서를 검증(검증을 끄는 옵션은 없음) |
| `WithHTTPClient(hc)` | SDK가 점검할 수 있는 클라이언트만: Transport가 없거나 `*http.Transport`(InsecureSkipVerify 불가), Jar·CheckRedirect 없음. 복사해서 쓰며 리다이렉트를 따르지 않음 |
| `WithTrustedHTTPClient(hc)` | 점검할 수 없는 RoundTripper(계측 래퍼 등)를 명시적으로 허용. 리다이렉트·재시도·인증 변경을 하지 않는다는 것은 호출자가 보장해야 함(중복 발송·키 노출 위험) |
| `WithLogger(*slog.Logger)` | 없음(로그 안 남김) |
| `WithHooks(hooks...)` | 없음 ([관측](#관측)) |
| `WithAppInfo(name, version)` | 없음. `User-Agent` 끝에 `app/<name>-<version>`을 붙입니다 |

모든 요청에는 `User-Agent: bizgo-sdk-comm-go/<버전> go/<버전> (<os>; <arch>)`와 `X-Bizgo-Client: bizgo-sdk-comm-go/<버전>`이 붙습니다(비즈고의 SDK 사용 현황 집계용, 호스트명 등 자세한 값 없음). `WithAppInfo`의 이름·버전에는 이메일·전화번호 같은 개인정보를 넣지 마세요.

> **접수 ≠ 발송 완료.** `Send.*`의 결과는 접수 결과입니다. 최종 결과는 [리포트](#리포트)로 받습니다.

## 발송

모든 채널은 `client.Send.Omni()` 하나로 보냅니다. `Messages`에 여러 개를 넣으면 **앞 메시지가 실패할 때 다음 메시지로 대체발송**됩니다.

```go
result, err := client.Send.Omni(ctx, bizgo.OmniParams{
	To: []bizgo.Destination{{To: "01000000000", ReplaceWords: map[string]string{"name": "홍길동"}}}, // 최대 200명
	Messages: []bizgo.ChannelMessage{
		&bizgo.AlimtalkMessage{SenderKey: "SENDER_KEY_EXAMPLE", TemplateCode: "TEMPLATE_CODE_EXAMPLE", MsgType: "AT", Text: "#{name}님, 주문이 접수되었습니다."},
		&bizgo.SMSMessage{From: "01000000000", Text: "#{name}님, 주문이 접수되었습니다."}, // 알림톡 실패 시
	},
	IdempotencyKey: "order-20260923-0001", // 권장: 재시도해도 중복 발송되지 않음
	Ref:            "order-20260923-0001", // 리포트에 그대로 돌아오는 참조값
})
if err != nil {
	return err
}
fmt.Println(result.MsgKeys())
```

| 채널 | 모델 (스펙 이름) | 간편 메서드 |
|---|---|---|
| SMS (90byte) | `SMSMessage` (`SmsMessage`) | `Send.SMS()` |
| LMS (2,000byte) | `MMSMessage` (`FileKey` 없음) | `Send.LMS()` |
| MMS | `MMSMessage` (`FileKey` 최대 3개) | `Send.MMS()` |
| 국제문자 | `InternationalMessage` | |
| RCS | `RCSMessage` (`RcsMessage`) | |
| 카카오 알림톡 | `AlimtalkMessage` | |
| 카카오 브랜드메시지 | `BrandMessage` | |
| 네이버 톡톡 | `NaverTalkMessage` | |

- 모델은 `spec/openapi.yaml`에서 생성합니다. Go 이름은 Go 관례를 따릅니다(`SmsMessage` → `SMSMessage`, `msgKey` → `MsgKey`, `infobankTrId` → `InfobankTrID`). JSON 필드 이름은 API 이름 그대로입니다.
- **설정한 필드만 보냅니다.** 선택 문자열은 빈 값이면 보내지 않고, 선택 숫자·불리언은 포인터입니다(`bizgo.Ptr(0)`). 스펙의 기본값을 임의로 보내지 않습니다.
- 예외 하나: 비즈고는 `idempotencyKey`만 있고 `idempotencyTtl`이 없는 요청을 A309로 거절합니다. 그래서 `IdempotencyKey`를 넣고 `IdempotencyTTL`을 비워 두면(nil) `bizgo.DefaultIdempotencyTTL`(86400초, 24시간)을 채워 보냅니다. `Send.*`, `Send.Request`, `Send.Bulk`의 청크, 본문에 두 필드가 모두 있는 생성 operation에 똑같이 적용됩니다. 직접 지정한 값(`bizgo.Ptr(0)` 포함)은 그대로 보내고, 키가 없으면 TTL을 더하지 않습니다. 넘긴 요청 구조체는 바꾸지 않습니다(복사본에 채움).
- 검증에 실패하면 아무것도 보내지 않고 `*bizgo.ValidationError`(필드 경로와 이유 목록, 입력값은 없음)를 돌려줍니다. 요청 모델의 `Validate()`로 미리 검사할 수도 있습니다.
- **조건부 필수**(스펙의 `x-sdk-required-if`)도 보내기 전에 검사합니다. 예: 알림톡 전문 발송(`SendType` 없음)은 `MsgType`(`AT` 또는 `AI`)과 `Text`가 필수이고(없으면 서버가 A523으로 거절), 템플릿 자동 치환 발송(`SendType: "template"`)은 모든 수신자의 `ReplaceWords`가 필수이며 `MsgType`·`Text`는 필요 없습니다. `MsgType`은 템플릿 조회 결과(`AlimtalkTemplate.MsgType`)의 값을 그대로 쓸 수 있습니다. 브랜드메시지 `SendType`별 필드, 버튼 `WL`의 `urlPc`·`urlMobile`, RCS `header`가 `1`이면 `footer`, 상담톡 `msgType`별 첨부도 같은 방식입니다. 오류에는 경로와 조건만 담깁니다(예: `messageFlow[0].alimtalk.msgType: sendType != template이면 필수입니다`).
- JSON으로 만든 요청은 `bizgo.ParseSendOmniRequest(data)`로 읽어 `client.Send.Request(ctx, req)`로 보냅니다. 모르는 필드(`templatecode` 같은 오타)는 거절합니다.
- 모델과 파라미터 구조체(`SMSParams`, `BulkParams`, `MOHistoryParams`, 생성된 `<OperationID>Params` 등)를 `%v`/`%+v`/`%#v`로 출력하면 전화번호 필드(`to`, `from`, `phoneNumber` 등)는 가려지고(11자리 이상 `010****0000`, 8~10자리는 절반 이상 `158****0`, 7자리 이하는 전부), 메시지 내용·토큰은 길이만, 수신자·메시지 목록은 개수만 보입니다. 사람 이름·이메일(`userName`, `nickname`, `email` 등)은 첫 글자만, 토큰·인증번호·암호화된 인증정보(`token`, `unsubscribeAuthNumber`, `certResult`)는 `[REDACTED]`, `replaceWords`는 개수만 보입니다. `log/slog`(JSON 핸들러 포함)도 같은 마스킹된 값을 남깁니다(`LogValue`). 필드 값 자체는 그대로입니다. 필드 이름으로 판단하므로 `ref` 같은 다른 필드에 개인정보를 넣지 마세요.

### 대량 발송

수신자 수에 제한이 없습니다. `ChunkSize`(1~200, 기본 200)씩 나눠 `Concurrency`(기본 4)개씩 동시에 보냅니다.

```go
recipients := bizgo.To("01000000000", "01000000001", "01000000002") // 수만 명도 가능
bulk, err := client.Send.Bulk(ctx, bizgo.BulkParams{
	To:                   recipients,
	Messages:             []bizgo.ChannelMessage{&bizgo.SMSMessage{From: "01000000000", Text: "9월 이벤트 안내"}},
	IdempotencyKeyPrefix: "campaign-sep", // 청크마다 campaign-sep-200-<시작 인덱스>-<해시>
	IdempotencyTTL:       bizgo.Ptr(3600), // 생략하면 bizgo.DefaultIdempotencyTTL(86400초)
})
if err != nil {
	return err // 요청이 잘못됨: 아무것도 보내지 않음
}
for _, e := range bulk.Errors { // 실패한 청크: 청크 번호와 수신자 인덱스 범위(번호는 없음)
	log.Println("chunk", e.Chunk, "recipients", e.Start, "-", e.End-1, e.Err)
}
fmt.Println(len(bulk.Succeeded()), len(bulk.Duplicates()), len(bulk.Failed()), len(bulk.MsgKeys()))
```

- 보내기 전에 모든 청크를 검증합니다. 하나라도 잘못되면 아무것도 보내지 않습니다(오류의 인덱스는 전체 목록 기준).
- 한 청크가 실패해도(오류, 응답 형식 오류, panic, 취소) 나머지는 계속 보내고, 이미 접수된 결과는 잃지 않습니다.
- 멱등성 키는 `<prefix>-<chunkSize>-<시작 인덱스>-<hash8>`입니다(hash8: 청크 수신번호를 `\n`으로 이은 값의 SHA-256 앞 8자리). **다시 실행할 때는 같은 목록·같은 `ChunkSize`로** 실행하면 이미 접수된 청크의 수신자는 `Duplicates()`(수신자별 A301)로 돌아오고 두 번 발송되지 않습니다. 수신자가 모두 A301인 청크도 오류가 아닙니다. 실패한 청크만 다시 보내려면 `Errors`의 청크 번호(인덱스 범위)를 쓰세요. 키는 200자 이하여야 합니다. `IdempotencyTTL`을 비워 두면 청크마다 `DefaultIdempotencyTTL`(86400초)을 보냅니다.
- 키가 있으면 타임아웃·5xx도 재시도하고, 없으면 429만 재시도합니다. [속도 제한](#속도-제한)(수신자 수 기준)이 함께 적용되어 200명 청크는 기본 한도에서 초당 1개씩 나갑니다.

### 이미지

```go
f, err := os.Open("banner.jpg")
if err != nil {
	return err
}
defer f.Close()
uploaded, err := client.Files.UploadMMS(ctx, bizgo.UploadParams{File: f}) // jpg, 최대 300KB
if err != nil {
	return err
}
_, err = client.Send.MMS(ctx, bizgo.MMSParams{To: bizgo.To("01000000000"), From: "01000000000", Text: "이미지 안내", FileKeys: []string{uploaded.FileKey}})
if err != nil {
	return err
}
_, _ = client.Files.UploadRCS(ctx, bizgo.UploadParams{File: card})                                 // → .Media
_, _ = client.Files.UploadBrandMessage(ctx, bizgo.BrandImageWide, bizgo.UploadParams{File: wide}) // → .ImgURL
```

파일은 한 번만 읽어 재시도에 다시 씁니다. 업로드는 HTTP 429일 때만 재시도합니다. 읽을 수 없는 파일은 `*ValidationError`이며 메시지에는 파일 이름만 있습니다(전체 경로 없음).

## 전체 API

P0 편의 메서드(`Send`, `Files`, `Reports`, `Messages`) 밖의 모든 operation은 스펙의 `x-sdk-*` 메타데이터로 **생성**됩니다: `client.<리소스>.<메서드>(ctx, 경로 파라미터..., 본문, 파라미터)`.

| 리소스 | 메서드 |
|---|---|
| `Reservations`, `Reservations.Recipients` | 예약 발송 등록·목록·조회·수정·취소·일시정지·재개, 수신자 추가·목록·삭제 |
| `Kakao.Senders`, `.Categories`, `.Groups`, `.Sanctions` | 발신프로필 토큰·등록·조회·목록·복구, 카테고리, 그룹, 제재 조회 |
| `Alimtalk.Templates`, `.TemplateCategories`, `.PublicTemplates` | 템플릿 등록·수정·조회·목록·변경 목록·삭제·검수 요청(`RequestInspectionWithFiles` 첨부)·검수 취소 |
| `BrandMessage.GroupSends`, `.Audience`, `.Templates`, `.GroupTags`, `.FriendGroups`, `.Videos`, `.Permissions`, `.MarketingAgreements`, `.UnsubscribeContents` | 동보 발송(생성·일시정지·재개·종료), 대상 확인, 템플릿, 그룹 태그, 친구 그룹, 동영상, 발송 권한, 마케팅 수신 동의 증빙, 수신거부 문구 |
| `RCS.Brands`, `.Chatbots`, `.Templates`, `.TemplateForms`, `.TemplateImages`, `.CommonFormats` | 브랜드·대화방 조회·수정(multipart), 템플릿 등록·수정·승인 취소·삭제, 양식, 이미지 |
| `Insights.Alimtalk`, `.BrandMessage`, `.RCS` | 채널별 인사이트(발송·반응·시간대·템플릿·버튼·메뉴) |
| `Counsel.Messages`, `.Sessions`, `.Users`, `.Channels`, `.ConsultTime`, `.SystemMessages`, `.Files`, `.Certs` | 상담톡 Plain/Rich 발송, 세션 종료, 사용자 차단, 채널 활성화, 상담 시간, 시스템 메시지, 파일, 본인인증 조회 |
| `Files` | 알림톡 템플릿·아이템하이라이트 이미지, 브랜드메시지 이미지 종류별 업로드(생성 메서드) |

```go
// 알림톡 템플릿: 목록 전체 순회 (offset/limit 자동)
for tpl, err := range client.Alimtalk.Templates.IterList(ctx, bizgo.ListAlimtalkTemplatesParams{SenderKey: "SENDER_KEY_EXAMPLE"}) {
	if err != nil {
		return err
	}
	fmt.Println(tpl.TemplateCode, tpl.TemplateName)
}

// 예약 발송: 결과에 resvKey와 수신자별 접수 결과가 함께 옵니다 (x-sdk-result: data)
resv, err := client.Reservations.Create(ctx, &bizgo.ReservationCreateRequest{
	Destinations: bizgo.To("01000000000"),
	MessageFlow:  []bizgo.ReservationMessageFlowItem{{SMS: &bizgo.SMSMessage{From: "01000000000", Text: "예약 안내"}}},
	ResvSendTime: "2026-10-01 10:00:00",
})
if err != nil {
	return err
}
if _, err := client.Reservations.Cancel(ctx, resv.ResvKey); err != nil {
	return err
}

// 인사이트: 날짜는 time.Time으로 받아 KST yyyyMMdd로 보냅니다
stats, err := client.Insights.Alimtalk.Get(ctx, bizgo.GetAlimtalkInsightParams{
	StartDate: bizgo.Date(2026, 9, 1), EndDate: bizgo.Date(2026, 9, 23), SenderKey: []string{"SENDER_KEY_EXAMPLE"},
})
if err != nil {
	return err
}
fmt.Println(len(stats.Statistics))

// 상담톡 발송 (속도 제한 send 버킷, 비용 1)
_, err = client.Counsel.Messages.SendPlain(ctx, &bizgo.CounselPlainMessageRequest{
	UserKey: "USER_KEY_EXAMPLE", SenderKey: "SENDER_KEY_EXAMPLE", MsgType: "TEXT", Message: "상담원이 연결되었습니다.",
})
if err != nil {
	return err
}
```

- **인자**: 경로 파라미터(순서대로) → 요청 본문(`*<스펙 모델>`) → 쿼리·헤더 파라미터(`<OperationID>Params`). 필수 값이 비었거나 스펙 제약(허용값·범위·길이·형식)에 맞지 않으면 보내기 전에 `*ValidationError`입니다. 경로 값은 한 세그먼트로 인코딩하며 `.`/`..`/빈 값은 거부합니다.
- **반환**: 스펙의 `x-sdk-result`(기본 `data.data`) 부분입니다. 데이터가 없는 operation은 `error`만 돌려줍니다. 성공 응답에 해당 부분이 없어도 `nil`을 돌려주지 않고 빈 값(목록은 빈 슬라이스)을 돌려주므로 nil 검사 없이 필드에 접근할 수 있습니다.
- **페이지**: `x-sdk-pagination`이 있는 목록은 `Iter<메서드>`가 있습니다(`iter.Seq2[항목, error]`). cursor는 `hasNext`/커서가 없거나 움직이지 않으면, page·offset은 빈 페이지·요청 크기보다 짧은 페이지·`total` 도달·`hasNext=false`에서 멈춥니다. 넘긴 파라미터 값은 바꾸지 않습니다.
- **multipart**(RCS 브랜드·대화방 수정, 템플릿 이미지, 상담톡 파일 등): 파일 필드는 `*bizgo.UploadFile{Reader: r}` 또는 `{Path: "..."}`입니다. 한 번 읽어(파일당 최대 100MB) 재시도에 다시 씁니다. 객체 필드는 스펙대로 JSON 파트로 보냅니다.
- **재시도**: `x-sdk-retry`대로 `safe`(조회, 같은 결과를 내는 PUT/DELETE)는 429·5xx·네트워크 오류, `rate_limit_only`(생성·발송·변경 요청)는 429만 재시도합니다.
- `bizgo.NewClient`로 만들지 않은 `bizgo.Client{}`의 메서드는 panic 대신 `*ConfigurationError`, nil `ctx`는 `*ValidationError`를 돌려줍니다.
- `bizgo.Operations()`는 operation 목록(operationId, 리소스.메서드, 경로 템플릿, 재시도, 속도 제한 버킷, 페이지 방식)을 돌려줍니다.
- 문서끼리 다르거나 확인되지 않은 부분은 각 메서드·필드 문서에 `확인되지 않음:`으로 적혀 있습니다(스펙의 `x-unverified`).

## 리포트

콘솔에서 API Key별로 리포트 수신 방식(POLLING 또는 WEBHOOK)을 정합니다.

**Polling** — 처리에 성공한 배치만 수신 확인합니다. 처리 함수가 오류를 돌려주면 같은 배치를 다시 받습니다.

```go
n, err := client.Reports.Consume(ctx, func(ctx context.Context, reports []bizgo.Report) error {
	for _, r := range reports {
		if err := db.Upsert(ctx, r.MsgKey, r.ReportCode); err != nil { // 같은 리포트가 다시 올 수 있으니 upsert
			return err
		}
	}
	return nil
}, nil)
if err != nil {
	return err
}
fmt.Println(n, "건 처리")
```

**개별 조회** — `client.Reports.Inquiry(ctx, msgKey)` (30일 이내)

## 웹훅

**리포트·MO** — 서명을 검증하고 5초 안에 `{"msgKey": ...}`로 응답합니다. `ReportHandler`/`MOHandler`가 이 규약을 처리합니다(서명 오류 401, 본문 오류 400, 처리 함수 오류 500). 웹훅 secret은 비즈고에 요청해 별도로 받습니다.

```go
receiver, err := bizgo.NewWebhookReceiver([]byte(os.Getenv("BIZGO_WEBHOOK_SECRET")))
if err != nil {
	return err
}
mux := http.NewServeMux()
mux.Handle("/bizgo/report", receiver.ReportHandler(func(ctx context.Context, r *bizgo.ReportWebhookPayload) error {
	return enqueue(ctx, r) // 무거운 처리는 비동기로
}))
server := &http.Server{
	Addr:              ":8080",
	Handler:           mux,
	ReadHeaderTimeout: 5 * time.Second,  // 헤더를 아주 천천히 보내는 요청이 연결을 붙잡지 않도록 제한
	ReadTimeout:       10 * time.Second, // 본문을 아주 천천히 보내는 요청도 끊음 (본문은 최대 1MB)
	WriteTimeout:      10 * time.Second,
}
log.Fatal(server.ListenAndServeTLS("server.crt", "server.key")) // HTTPS
```

SDK 핸들러는 본문 크기(1MB)만 제한하고 읽기 시간은 제한하지 않습니다. 시간 제한은 `http.Server`의 `ReadHeaderTimeout`/`ReadTimeout`으로 거세요(기본값 0은 무제한). 비즈고는 5초 안에 응답을 기다리므로 처리 함수는 빨리 끝내야 합니다.

프레임워크를 직접 쓸 때는 `receiver.Report(headers, body)` / `receiver.MO(headers, body)`와 `bizgo.NewWebhookAck(msgKey)`를 씁니다.

**상담톡** — 웹훅 7종(`CounselMessage`, `CounselResult`, `CounselReference`, `CounselExpiredSession`, `CounselSeenInfo`, `CounselPersonalInfo`, `CounselCertResult`)마다 파서(`bizgo.ParseCounselMessageWebhook`), receiver 메서드(`receiver.CounselMessage(headers, body)`), `http.Handler`(`receiver.CounselMessageHandler(fn)`)가 있습니다. 응답은 `{"code":"A000","result":"Success"}`(`bizgo.NewCounselWebhookAck()`)입니다. 등록한 URL 뒤에 `/cstalk/message`, `/cstalk/result` 등이 붙어 호출됩니다.

상담톡 웹훅에는 서명이 없습니다(서명은 리포트·MO 웹훅에만 적용). SDK는 상담톡 웹훅의 서명을 요구하거나 검사하지 않고, 서명 헤더가 와도 무시합니다. 본문 검사(최대 1MB, JSON 깊이 64단계, 필드 타입)는 그대로 적용하고 타입이 있는 payload를 돌려줍니다(본문 오류는 400, 처리 함수 오류는 500).

리포트·MO와 함께 받는다면 같은 receiver의 Handler를 씁니다.

```go
receiver, err := bizgo.NewWebhookReceiver([]byte(os.Getenv("BIZGO_WEBHOOK_SECRET"))) // secret은 리포트·MO 검증에 쓰입니다
if err != nil {
	return err
}
http.Handle("/bizgo/cstalk/message", receiver.CounselMessageHandler(func(ctx context.Context, m *bizgo.CounselMessageWebhookPayload) error {
	log.Println("상담 메시지", m.MsgKey) // 본문(상담 내용·개인정보)은 로그에 남기지 않습니다
	return nil
}))
```

상담톡만 받는다면 webhook secret이 필요 없습니다. receiver 없이 패키지 함수 `bizgo.ParseCounsel*Webhook(body)`와 `bizgo.NewCounselWebhookAck()`를 씁니다.

```go
http.HandleFunc("/bizgo/cstalk/message", func(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bizgo.MaxWebhookBodyBytes))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	m, err := bizgo.ParseCounselMessageWebhook(body) // 크기·깊이·타입 오류는 bizgo.ErrWebhook
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	log.Println("상담 메시지", m.MsgKey)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(bizgo.NewCounselWebhookAck()) // {"code":"A000","result":"Success"}
})
```

> **보안.** 리포트·MO 웹훅은 항상 서명과 timestamp 허용 오차를 검증합니다(서명 오류 401). 운영 환경에서는 모든 웹훅 엔드포인트에 HTTPS, 비즈고 웹훅 발신 IP 허용 목록, `msgKey` 기준 중복 제거를 함께 적용하고, 중요한 판단은 리포트·상태 조회 API로 결과를 확인하세요.
> 웹훅 timestamp는 1~16자리 숫자만, 허용 오차는 0보다 큰 값만(끄려면 `bizgo.NoWebhookTolerance`) 받습니다. 본문은 최대 1MB, JSON 깊이 64단계입니다.

## 조회

```go
statuses, err := client.Messages.Status(ctx, msgKey) // 단건 상태 (대체발송 시 채널별 항목)
if err != nil {
	return err
}
fmt.Println(len(statuses))
_, _ = client.Messages.StatusByRequestID(ctx, requestID) // 동보 요청 전체 (msgKey에서 끝 3자리를 뺀 값)
_, _ = client.Messages.Statistics(ctx, bizgo.StatisticsParams{StartDate: bizgo.Date(2026, 9, 1), EndDate: bizgo.Date(2026, 9, 23)})

since := time.Date(2026, 9, 23, 9, 0, 0, 0, bizgo.KST)
for m, err := range client.Messages.IterHistory(ctx, bizgo.HistoryParams{RequestTime: since, ServiceTypes: []string{"SMS", "ALIMTALK"}}) {
	if err != nil {
		return err
	}
	fmt.Println(m.MsgKey) // 페이지(lastSeq)를 자동으로 따라감
}
for mo, err := range client.Messages.IterMOHistory(ctx, bizgo.MOHistoryParams{OccurredTime: since}) { // MO(수신) 이력
	if err != nil {
		return err
	}
	fmt.Println(mo.MsgKey)
}
```

- 시각은 `time.Time`으로 받아 KST로 바꿔 보냅니다(발송 이력 `yyyy-MM-ddTHH:mm:ss`, MO 이력 `+09:00` 포함, 통계 `YYYYMMDD`).
- `Limit`은 1~1000(0이면 서버 기본값 100)입니다.

## 오류 처리

```go
_, err := client.Send.Omni(ctx, params)
var ve *bizgo.ValidationError
var apiErr *bizgo.APIError
var invalid *bizgo.InvalidResponseError
switch {
case errors.As(err, &ve): // 보내기 전 검증 실패. ve.Problems: 필드 경로와 이유
case errors.Is(err, bizgo.ErrAuthentication): // 키가 틀렸거나 IP가 등록되지 않음
case errors.Is(err, bizgo.ErrRateLimit): // 자동 재시도 후에도 한도 초과
case errors.Is(err, bizgo.ErrDuplicateRequest): // 요청 단위 A301(data.code). 수신자별 A301은 오류가 아니라 result.Duplicates()
	if errors.As(err, &apiErr) && apiErr.AlreadyAccepted {
		log.Println("재시도 전에 보낸 요청이 이미 접수됨") // 상태 조회로 결과 확인
	}
case errors.As(err, &apiErr): // 그 밖의 거절. apiErr.Code, .Layer, .Description, .TrackingID
case errors.As(err, &invalid): // 응답 형식 오류. 발송이면 접수됐을 수 있음 → 상태 조회로 확인 (invalid.TrackingID)
case errors.Is(err, bizgo.ErrConnection): // 응답을 못 받음. 발송은 접수됐을 수도 있음 → 상태 조회로 확인
}
```

| 종류 | 타입 / `errors.Is` |
|---|---|
| 설정 오류 | `*ConfigurationError` / `ErrConfiguration` |
| 검증 오류 | `*ValidationError` / `ErrValidation` |
| API 오류 | `*APIError` / `ErrBadRequest`, `ErrAuthentication`, `ErrPermissionDenied`, `ErrNotFound`, `ErrDuplicateRequest`, `ErrRateLimit`, `ErrInternalServer`, `ErrAPI` |
| 연결 오류 | `*ConnectionError`(`Timeout`) / `ErrConnection`, `ErrTimeout`, `context.Canceled` 등 |
| 응답 형식 오류 | `*InvalidResponseError`(`HTTPStatus`, `TrackingID`, `Body`) / `ErrInvalidResponse` (리다이렉트, 16MB 초과, JSON 깊이 64 초과 포함) |
| 웹훅 검증 실패 | `*WebhookVerificationError` / `ErrWebhook` |
| 대량 발송 청크 panic·`runtime.Goexit` | `*PanicError`(`Type`: panic 값의 타입만, 값은 없음) / `ErrChunkPanic` |

- 응답 봉투의 최상위·`common`·`data`에 같은 키가 두 번 있으면(마지막 값으로 실패가 성공으로 바뀔 수 있으므로) `*InvalidResponseError`입니다.
- 응답은 두 단계로 판정합니다: `common.authCode`(게이트웨이: 인증·형식) → `data.code`(상품 처리). `apiErr.Layer`가 `gateway`/`service`입니다. 같은 코드라도 단계마다 뜻이 다릅니다(게이트웨이 A401 = 인증 실패, 상품 A401 = paymentCode 오류).
- 발송 요청이 성공해도 **일부 수신자는 거절될 수 있습니다.** 항상 `result.Failed()`를 확인하세요.
- **같은 `IdempotencyKey`로 다시 보내면**(타임아웃 뒤 재전송 등) 요청은 성공(HTTP 200)하고, 이미 접수된 수신자는 수신자별 코드 A301로 `result.Duplicates()`에 들어갑니다. `Failed()`(거절)에도 `Succeeded()`(이번 요청에서 새로 접수)에도 없고, `MsgKeys()`는 `Succeeded()`의 키만 돌려줍니다. 수신자는 `Succeeded()`·`Duplicates()`·`Failed()` 중 정확히 하나에 들어갑니다(sandbox 2026-09-28 확인).
- 오류는 `encoding/json`과 `encoding/gob`으로 직렬화·복원할 수 있습니다(작업 큐 등). 복원해도 `errors.Is`가 같게 동작합니다. `Body`(응답 원문)도 포함되니 그대로 로그에 남기지 마세요.

## 재시도와 타임아웃

| 요청 | 자동 재시도 |
|---|---|
| 조회, 리포트, 수신 확인, 같은 결과를 내는 수정·삭제 (`safe`) | 429, 500/502/503/504, 네트워크 오류 |
| 발송 (`IdempotencyKey` 있음) | 429, 500/502/503/504, 네트워크 오류 |
| 발송 (`IdempotencyKey` 없음), 업로드, 생성·변경 요청 (`rate_limit_only`) | 429만 (중복 실행 방지) |

기본값은 최대 2회 재시도(`Retry-After` 우선 — 0~60초의 유한한 숫자만, 그 밖의 값은 무시; 없으면 0.5초부터 지수 백오프·최대 8초·지터), 시도당 타임아웃 30초(연결 5초)입니다.
3xx 응답은 따르지도 재시도하지도 않습니다. 재시도한 요청이 요청 단위 `A301`(`data.code`, `ErrDuplicateRequest`)을 받으면 `apiErr.AlreadyAccepted`가 참입니다(이전 시도가 이미 접수됨). 수신자별 A301은 `result.Duplicates()`입니다.
`ctx`의 취소·마감은 재시도 대기 중에도 바로 반영됩니다.

## 속도 제한

클라이언트마다 토큰 버킷 2개로 요청 전에 기다립니다(재시도 포함 시도마다). 기본으로 켜져 있습니다.

| 버킷 | 기본 한도 | 비용 | 대상 |
|---|---|---|---|
| send | 초당 200 **메시지** | 요청의 수신자 수(최소 1) | 스펙에서 `x-sdk-rate: send`인 operation: `sendOmni`(`Send.Omni/SMS/LMS/MMS/Request/Bulk`), `createReservation`, `addReservationRecipients`, `createBrandMessageGroupSend`, `sendCounselPlain`, `sendCounselRich` (수신자 목록이 없는 상담톡·친구 그룹 동보는 1) |
| other | 초당 5 요청 | 1 | 그 밖의 모든 API |

- 버킷 용량은 초당 한도와 같고 가득 찬 상태로 시작합니다(비즈고 서버도 토큰 버킷). 수신자 200명 요청은 토큰 200개를 쓰므로 초당 1건이 나갑니다. 비용이 용량보다 크면 버킷이 가득 찰 때까지 기다린 뒤 보내고 부족분은 빚으로 남깁니다.
- `bizgo.WithRateLimit(100, 5)`로 바꾸고 `bizgo.WithoutRateLimit()`로 끕니다.
- **한도는 계정 단위입니다.** 이 제한은 한 프로세스의 한 클라이언트만 조절합니다. 여러 클라이언트·프로세스·서버가 같은 키를 쓰면 한도를 나눠 낮추거나 직접 조율하세요. 서버가 429를 주면 기존대로 재시도합니다.

## 테스트

`bizgotest` 패키지로 네트워크·API Key 없이 자기 코드를 테스트합니다(운영 코드에서는 import하지 않아도 됩니다).

```go
func TestSignup(t *testing.T) {
	fake := bizgotest.New()
	client, err := fake.Client() // placeholder 키, sandbox URL, 속도 제한 끔, 네트워크 없음
	if err != nil {
		t.Fatal(err)
	}
	fake.On("sendOmni").SendResult("A000", "A306")         // 두 번째 수신자 거절 ("A301"이면 Duplicates)
	fake.On("getReportPolling").Fail(bizgo.LayerService, 200, "A020") // 오류 주입 (RateLimit)

	result, err := client.Send.SMS(context.Background(), bizgo.SMSParams{To: bizgo.To("01000000000", "01000000001"), From: "01000000000", Text: "hi"})
	if err != nil || len(result.Failed()) != 1 {
		t.Fatal(result, err)
	}
	req := fake.LastRequest() // OperationID, Method, Path, PathTemplate, Query, JSON, Form (헤더 값은 기록하지 않음)
	if req.OperationID != "sendOmni" || req.JSON.(map[string]any)["destinations"] == nil {
		t.Fatal(req)
	}

	// 웹훅 핸들러 테스트: 서명된 요청 만들기
	receiver, err := bizgo.NewWebhookReceiver([]byte("test-webhook-secret"))
	if err != nil {
		t.Fatal(err)
	}
	httpReq, err := bizgotest.NewWebhookRequest(context.Background(), "/bizgo/report", []byte("test-webhook-secret"), map[string]any{"msgKey": "KEY001"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	receiver.ReportHandler(nil).ServeHTTP(rec, httpReq)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
```

- 스텁이 없으면 operation별 성공 응답을 돌려줍니다: 발송(`sendOmni`, `createReservation`, `addReservationRecipients`)은 수신자마다 `A000`과 가짜 `msgKey`(`createReservation`은 `resvKey`도), MMS/RCS 업로드는 가짜 `fileKey`/`media`, 그 밖은 `data.data = {}`. 모르는 경로는 404(A404)입니다.
- `On(operationId)` / `OnRoute(method, 경로 템플릿)` 뒤에 `Respond`, `RespondWithHeader`, `Data`, `SendResult`, `Fail`(429·5xx는 `Retry-After: 0`), `NetworkError`, `Timeout`, `Default`를 이어 붙입니다. 차례대로 쓰고 마지막 응답이 반복됩니다.
- `bizgotest.SignWebhook(secret, payload, ts)`는 헤더와 본문을, `bizgotest.Signature`/`SignatureBase64`는 서명 값을 만듭니다.

## 관측

`bizgo.WithHooks`로 요청 시작·종료를 받습니다. 재시도를 포함한 API 호출 한 번에 시작·종료가 한 번씩입니다.

```go
metrics := bizgo.Hooks{
	OnRequestEnd: func(ctx context.Context, e bizgo.RequestEvent) {
		// e.OperationID ("sendOmni"), e.Operation ("send.omni"), e.Method, e.PathTemplate ("/api/comm/v1/report/inquiry/{msgKey}"),
		// e.Status, e.Layer, e.Code, e.ErrorKind, e.Success, e.Attempts, e.Duration
		log.Println(e.Operation, e.Status, e.Code, e.Attempts, e.Duration)
	},
}
observed, err := bizgo.NewClient(bizgo.WithHooks(metrics))
if err != nil {
	return err
}
_ = observed
```

- 이벤트에는 **본문, 쿼리, 헤더 값, 실제 경로 값, 전화번호, 키가 없습니다.** 경로는 템플릿만 넘깁니다.
- hook이나 로거에서 panic이 나도 SDK가 삼키며, 결과·재시도·대량 발송 결과에 영향을 주지 않습니다. 응답을 해석하지 못한 호출은 성공으로 보고하지 않습니다.
- **OpenTelemetry**: 별도 모듈 `github.com/icomm-api/bizgo-sdk-comm-go/otel`(패키지 `bizgootel`)이 hooks 위에서 span을 만듭니다. core 모듈의 의존성은 늘지 않습니다.

```bash
go get github.com/icomm-api/bizgo-sdk-comm-go/otel@v1.2.0   # SDK와 같은 버전
```

```go
import bizgootel "github.com/icomm-api/bizgo-sdk-comm-go/otel"

client, err := bizgo.NewClient(bizgo.WithHooks(bizgootel.Hooks(otel.GetTracerProvider())))
```

span 이름은 `bizgo <resource>.<method>`(예: `bizgo send.omni`), 속성은 `http.request.method`, `url.template`, `http.response.status_code`, `bizgo.operation_id`, `bizgo.code`, `bizgo.layer`, `bizgo.retry_count`, 실패 시 `error.type`입니다.

## 보안

- API Key는 **접두어 없이** 키 값만 씁니다(`Bearer`/`ApiKey` 접두어는 401). 환경변수나 시크릿 저장소에서 읽고, 코드·저장소·클라이언트 앱에 넣지 않습니다. 공백·줄바꿈·비ASCII 문자가 있으면 조용히 잘라내지 않고 설정 오류로 알립니다.
- SDK는 키, 요청 본문, 쿼리 문자열(전화번호가 들어갈 수 있음)을 로그나 오류 메시지에 남기지 않습니다. `WithLogger`로 준 로거에는 `METHOD path -> status (ms, attempt n)`만 남깁니다. `Client`의 `String`/`GoString`에도 키가 없습니다. 모델의 `String`은 전화번호를 가리고 내용은 길이만 보입니다.
- net/http의 `*url.Error`(쿼리 문자열이 포함된 전체 URL을 담음)는 오류 체인에서 제거합니다.
- `APIError.Body`/`InvalidResponseError.Body`에는 응답 원문(전화번호 포함 가능)이 있으니 그대로 로그에 남기지 마세요.
- `Authorization`, `User-Agent`, `X-Bizgo-Client`는 SDK가 요청마다 마지막에 설정하므로 옵션으로 바꿀 수 없습니다. `http://` base URL은 거부합니다(테스트용 localhost 제외). 리다이렉트는 따르지 않습니다(직접 넘긴 `http.Client`도 복사해서 적용).
- TLS 인증서 검증을 끄는 방법은 없습니다. 사내 CA는 `WithRootCAs`/`WithRootCAsFile`, 프록시는 `WithProxy`로 설정합니다. `WithHTTPClient`는 `InsecureSkipVerify`를 켠 `*http.Transport`, 점검할 수 없는 RoundTripper, 쿠키 Jar, CheckRedirect가 있는 클라이언트를 거부합니다. `WithTrustedHTTPClient`로 넘긴 RoundTripper는 SDK가 점검하지 않으므로 그 동작(재시도·리다이렉트·로그 등)과 TLS 설정은 호출자 책임이며, 그 RoundTripper는 `Authorization` 헤더와 본문을 봅니다.
- 응답 본문은 압축 해제 후 최대 16MB, 웹훅 본문은 최대 1MB, JSON 깊이는 64단계까지만 받습니다.
- 취약점 신고는 [SECURITY.md](SECURITY.md)를 참고하세요.

## 이전 SDK에서 옮기기

| infobank-omni-sdk-go | bizgo-sdk-comm-go |
|---|---|
| `github.com/icomm-api/infobank-omni-sdk-go/pkg/infobank` | `github.com/icomm-api/bizgo-sdk-comm-go` (패키지 `bizgo`) |
| `infobank.NewOmniClient(url, id, password)` + 토큰 발급 | `bizgo.NewClient()` + API Key 하나 (`BIZGO_API_KEY`, 토큰 발급 없음) |
| `https://omni.ibapi.kr` | `https://mars.ibapi.kr` (sandbox `https://sandbox-mars.ibapi.kr`) |
| 채널별 클라이언트 (`omniClient.SMS.SendMessage(&models.MMS{...})`) | `client.Send.Omni()` 하나, 채널은 메시지 모델로 구분 (`Send.SMS/LMS/MMS` 간편 메서드) |
| `models.OmniMessage{SMS: &models.OmniSMS{...}}` (OMNI 대체발송) | `Messages: []bizgo.ChannelMessage{&bizgo.AlimtalkMessage{...}, &bizgo.SMSMessage{...}}` |
| `omniClient.File.UploadFile(serviceType, msgType, path)` | `client.Files.UploadMMS / UploadRCS / UploadBrandMessage` |
| `omniClient.Report.InquiryReport(msgKey)` | `client.Reports.Inquiry(ctx, msgKey)`, Polling은 `Reports.Consume`, 웹훅은 `WebhookReceiver` |
| `omniClient.Form.RegisterForm(...)` | 없음 (알림톡·브랜드메시지는 템플릿 발송 사용, 템플릿 관리는 `client.Alimtalk.Templates` 등) |
| `{code, result, data}` 응답, `context` 없음 | `{common, data}` 응답(SDK가 풀어서 반환), 모든 호출이 `context.Context`를 받음 |
| 서드파티 의존성(validator, testify) | 런타임 의존성 없음 |

이전 SDK의 코드·예제에 있던 인증 정보나 전화번호는 그대로 옮기지 마세요.

## 개발

```bash
go vet ./... && go test -race ./...           # 네트워크·키 없이 mock 서버로 실행 (README의 Go 코드도 컴파일 검사)
golangci-lint run ./...
go generate ./...                             # spec/가 바뀌었을 때 (= go -C tools/gen run . -root ../..)
go -C tools/gen run . -root ../.. -check      # CI: 생성 코드가 spec과 같은지
govulncheck ./...
(cd otel && go test -race ./...)              # OpenTelemetry 어댑터 (별도 모듈)
```

생성기(`tools/gen`)와 OpenTelemetry 어댑터(`otel/`)는 별도 모듈이라 그 의존성은 SDK 사용자에게 전달되지 않습니다.

AI 코딩 도구로 이 저장소를 수정할 때의 규칙은 [AGENTS.md](AGENTS.md)에 있습니다.
SDK를 **사용하는** 코드를 AI로 작성할 때는 [llms.txt](llms.txt)를 컨텍스트로 넣으면 정확도가 높아집니다. 예제는 [examples/](examples/)에 있습니다.

## 라이선스

[Apache-2.0](LICENSE)
