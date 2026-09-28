# Changelog

이 프로젝트는 [Semantic Versioning](https://semver.org/lang/ko/)을 따릅니다.

## 1.2.0 - 2026-09-28

첫 공개 릴리스. 비즈고 커뮤니케이션 API(`https://mars.ibapi.kr`) 기준으로 새로 작성했습니다.
`infobank-omni-sdk-go`(OMNI API)의 후속이며 API가 호환되지 않습니다(README의 "이전 SDK에서 옮기기" 참고).
버전 번호는 비즈고 SDK 6종(언어별)과 맞춘 것이며, 1.0.0은 공개 배포되지 않았습니다(그 기록은 이 항목에 합쳤습니다).
태그: `v1.2.0`(SDK), `otel/v1.2.0`(OpenTelemetry 어댑터).

### 추가 (발송 규칙)
- `idempotencyKey`를 보내고 `idempotencyTtl`을 비워 두면 상수 `bizgo.DefaultIdempotencyTTL`(86400초)을 채워 보냅니다. 비즈고가 키만 있는 요청을 A309로 거절하는 것을 sandbox에서 확인했습니다(2026-09-28). `Send.Omni`/`SMS`/`LMS`/`MMS`, `Send.Request`, `Send.Bulk` 청크, 본문에 두 필드가 모두 있는 생성 operation에 적용되며, 직접 지정한 값(0 포함)은 그대로 보내고 키가 없으면 TTL을 더하지 않습니다. 넘긴 요청 구조체는 바꾸지 않습니다.
- 수신자별 A301은 `Duplicates()`: 같은 `idempotencyKey`로 다시 보내면 요청은 성공(HTTP 200)하고 이미 접수된 수신자의 `destinations[].code`가 A301입니다(sandbox 2026-09-28 확인). `SendResult.Duplicates()`·`BulkSendResult.Duplicates()`(청크 합산)로 돌려주며 `Failed()`(거절)·`Succeeded()`(이번 요청에서 새로 접수)·`MsgKeys()`에는 넣지 않습니다. 수신자가 모두 A301인 청크도 오류가 아닙니다. 요청 단위 A301(`data.code`)은 그대로 `ErrDuplicateRequest`입니다. `bizgotest`의 `SendResult("A301")`로 흉내 낼 수 있습니다
- 스펙 갱신: `idempotencyTtl` 설명·`x-verified`, 발송 예제, 에러코드 비고(A309), `idempotencyKey` 설명(수신자별 A301)
- 조건부 필수 검증: 스펙의 `x-sdk-required-if`를 생성기가 읽어 요청 본문별 규칙 표를 만들고, 보내기 전에 검사합니다(`*ValidationError`, 경로와 조건만 표시, 요청을 보내지 않음). **알림톡 전문 발송(`SendType` 없음)은 `MsgType`(`AT`/`AI`)과 `Text`가 필수**입니다(서버는 A523으로 거절, sandbox 2026-09-28 확인). 템플릿 자동 치환 발송(`SendType: "template"`)은 모든 `destinations[].replaceWords`가 필수이고 `MsgType`·`Text`는 필요 없습니다. 이 밖에 브랜드메시지 `sendType`별 필드, 브랜드 버튼 `WL`→`urlPc`·`urlMobile`/`AL`→`urlMobile`, RCS `header` `1`→`footer`, 상담톡 Plain `FILE`·Rich `msgType`별 첨부. 알림톡 전문 발송에는 `MsgType: "AT"`(또는 `"AI"`)를 넣으세요
- `AlimtalkTemplate.MsgType`(응답, 알려진 값 `AT`/`AI`): 조회한 템플릿의 값을 발송의 `AlimtalkMessage.MsgType`에 그대로 쓸 수 있습니다

### 추가 (전체 API와 편의 기능)
- 스펙의 **146개 operation 전체**: `x-sdk-*` 메타데이터로 생성한 리소스 메서드(`client.Alimtalk.Templates.List`, `client.Reservations.Create`, `client.BrandMessage.GroupSends.Create`, `client.RCS.Brands.Update`, `client.Insights.Alimtalk.Get`, `client.Counsel.Messages.SendPlain` 등). 경로·쿼리·헤더 파라미터(`<OperationID>Params`), 본문 검증, `x-sdk-result` 반환, `x-sdk-retry` 재시도, 페이지 순회 `Iter<Method>`(cursor·page·offset), multipart 업로드(`UploadFile`, JSON 파트), `bizgo.Operations()`
- 웹훅 9종: 상담톡 웹훅 7종의 파서·`http.Handler`(상담톡 웹훅에는 서명이 없으므로 서명을 검사하지 않고 서명 헤더는 무시, 본문 검사는 적용, webhook secret 없이 쓰는 패키지 함수 `ParseCounsel*Webhook`, `{"code":"A000","result":"Success"}` 응답). 리포트·MO 웹훅은 항상 서명 검증
- `Send.Bulk`: 수신자 수 제한 없는 청크 발송, 동시성, 멱등성 키 `<prefix>-<chunkSize>-<start>-<hash8>`, 청크별 오류·panic 격리, `BulkIdempotencyKey`
- 클라이언트 속도 제한(`WithRateLimit`, `WithoutRateLimit`): send 버킷 초당 200 메시지(수신자 수 기준, `x-sdk-rate: send`), other 버킷 초당 5 요청, 시도마다 대기
- `bizgotest` 패키지: 가짜 RoundTripper, 요청 기록(헤더 값 제외), operation별 기본 성공 응답, 오류 주입, `SignWebhook`
- `WithHooks`(operation, 경로 템플릿, 상태, 코드, 시도 횟수, 소요 시간; 본문·값 없음, panic 격리)와 별도 모듈 OpenTelemetry 어댑터 `github.com/icomm-api/bizgo-sdk-comm-go/otel`
- SDK 식별 헤더 `User-Agent: bizgo-sdk-comm-go/<ver> go/<ver> (<os>; <arch>)`, `X-Bizgo-Client`, `WithAppInfo(name, version)`
- 강화 규칙(SDK-DESIGN §12): 응답 해석 실패는 `InvalidResponseError`(상태·TrackingID·본문, 발송이면 접수 가능성 안내), 재시도 뒤 A301은 `AlreadyAccepted`, 3xx 재시도 안 함(메시지에 HTTP 상태), 압축 해제 후 16MB·JSON 깊이 64 제한, `Retry-After`는 0~60초 유한값만, 오류 JSON 직렬화, 모델 `String`의 전화번호 마스킹(`MaskPhone`), API Key를 잘라내지 않고 거부, 웹훅 timestamp 1~16자리·tolerance 검증(`NoWebhookTolerance`), 업로드 파일 오류는 파일 이름만 담은 검증 오류, 사용자 클라이언트의 쿠키 저장소 제거, README 코드 컴파일 테스트
- 브랜드메시지 이미지 종류 `catalog`, `catalog/oddFirst`
- 사용자 HTTP 클라이언트 정책(§12.6): `WithHTTPClient`는 점검 가능한 클라이언트만(쿠키 Jar·CheckRedirect·InsecureSkipVerify·점검 불가 RoundTripper 거부), `WithTrustedHTTPClient`(명시적 허용), `WithProxy`, `WithRootCAs`, `WithRootCAsFile`
- 마스킹 확장(§12.11): 사람 이름·이메일은 첫 글자, 토큰·인증번호·`certResult`는 `[REDACTED]`, `replaceWords`는 개수, 모델·파라미터·결과의 `LogValue`(slog)
- 검토 반영: 직접 작성·생성된 모든 파라미터 구조체의 `String`/`GoString` 마스킹, 짧은 번호 마스킹(8~10자리 절반 이상), 성공 응답에 `nil` 결과 없음(빈 값·빈 슬라이스, §12.19), 오류의 `encoding/gob` 왕복, `bizgo.Client{}`·nil ctx는 panic 대신 타입 있는 오류(리소스 필드는 값 타입), 청크 panic은 `*PanicError`(타입만)·`runtime.Goexit`도 실패 청크, panic으로 끝난 호출도 hook·span 종료, 응답 봉투의 중복 키 거부, README 웹훅 서버 예제에 `ReadHeaderTimeout`/`ReadTimeout`, `otel/v*` 태그 릴리스 검증(배포된 SDK로 tidy·테스트)

### 추가 (P0)
- `bizgo.NewClient` 와 옵션(`WithAPIKey`, `WithEnvironment`, `WithBaseURL`, `WithTimeout`, `WithMaxRetries`, `WithHTTPClient`, `WithLogger`), API Key 인증(환경변수 `BIZGO_API_KEY`), 운영·sandbox 환경
- 통합 발송 `Send.Omni`와 간편 메서드 `Send.SMS`/`LMS`/`MMS`, `Send.Request`/`ParseSendOmniRequest`, 대체발송, 치환, 멱등성 키
- 채널 모델: SMS, LMS/MMS, 국제, RCS, 알림톡, 브랜드메시지, 네이버 톡톡 (OpenAPI 스펙에서 생성, 응답 모델은 모르는 필드를 `Extra`에 보존)
- 보내기 전 검증: 필수·모르는 필드, 길이, 바이트 길이(EUC-KR/CP949), 개수(수신자 200명), 허용값, 형식, 채널 키 1개
- 이미지 업로드: MMS(300KB 검사), RCS, 브랜드메시지 6종
- 리포트: polling(`Consume`), 수신 확인, 개별 조회
- 조회: 상태(단건·요청별), 발송 이력·MO 이력(`iter.Seq2` 페이지 자동 순회), 통계
- 웹훅: 서명 검증, 리포트·MO 페이로드 파싱, `http.Handler`
- 오류 타입과 `errors.Is` 종류(게이트웨이/상품 단계 구분, 에러코드 설명), 재시도(429·5xx·네트워크, 발송은 멱등성 키가 있을 때만)
- 런타임 의존성 없음
