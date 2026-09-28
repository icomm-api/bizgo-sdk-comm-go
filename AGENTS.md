# AGENTS.md — bizgo-sdk-comm-go

AI 코딩 도구와 기여자가 이 저장소를 수정할 때 따르는 규칙입니다. SDK를 *사용하는* 코드를 쓸 때는 [llms.txt](llms.txt)를 보세요.
언어 공통 설계는 [bizgo-api-spec의 docs/SDK-DESIGN.md](https://github.com/icomm-api/bizgo-api-spec/blob/main/docs/SDK-DESIGN.md)를 따릅니다(§10 생성 메서드, §11 편의 기능, §12 강화 규칙 포함).

## 명령

```bash
go vet ./... && go test -race ./...            # 전부 mock(httptest, bizgotest). 네트워크·API Key 불필요. README의 Go 코드도 컴파일 검사
golangci-lint run ./...                        # 설정: .golangci.yml (CI는 v2.13.2)
go generate ./...                              # 생성 코드 재생성
go -C tools/gen run . -root ../.. -check       # CI: 생성 결과 전부(아래 *_gen.go, operations_gen_test.go, testdata/spec.json)가 커밋과 같은지
govulncheck ./...
(cd otel && go vet ./... && go test -race ./... && golangci-lint run --config ../.golangci.yml ./...)   # OpenTelemetry 어댑터(별도 모듈)
```

변경 후 위 명령이 모두 통과해야 합니다.

## 구조

```
spec/openapi.yaml, spec/error-codes.json   # bizgo-api-spec에서 복사한 스펙 (직접 수정 금지, 스펙 저장소에서 고친 뒤 복사)
tools/gen/                                 # 별도 모듈: 스펙 → 생성 코드. 의존성은 여기에만 둠
  models.go                                #   모델(+validate/Validate, String 마스킹, multipart writeForm)
  ops.go                                   #   x-sdk-* → 서비스·메서드·Iter·operation 표, 웹훅 파서
  testgen.go                               #   operation마다 호출 샘플(operations_gen_test.go)
  requiredif.go                            #   x-sdk-required-if 규칙 검사 → 요청 본문별 규칙 표(requiredIf<모델>)
models_gen.go        # 생성됨: 요청 모델(+validate), 응답 모델(+Extra, UnmarshalJSON), 채널 union(MessageFlowItem 등)
services_gen.go      # 생성됨: Services(리소스 트리), operation 표(op*), 모든 operation의 메서드와 Iter*, <OperationID>Params
webhooks_gen.go      # 생성됨: 상담톡 웹훅 파서·Handler, webhookTable
operations_gen_test.go # 생성됨: 생성된 메서드마다 placeholder 인자로 한 번 호출 (operations_test.go가 사용)
errorcodes_gen.go    # 생성됨: data.code → (HTTP 상태, 설명)
euckr_gen.go         # 생성됨: CP949로 인코딩할 수 있는 BMP 문자 비트맵
testdata/spec.json   # 생성됨: 테스트용 operation·웹훅 메타데이터, 스펙 예제
client.go            # NewClient, Option, Environment, API Key·base URL 검증, User-Agent/X-Bizgo-Client, 리다이렉트 거부
transport.go         # 헤더, 재시도, 속도 제한 대기, 응답 봉투 해석(16MB, JSON 깊이 64), 오류 매핑, 로그, hooks 호출
operation.go ratelimit.go hooks.go         # operation 메타데이터·Operations(), 토큰 버킷 2개, Hooks/RequestEvent
runtime.go           # 생성 코드용 헬퍼: buildPath, 쿼리 형식, multipart formWriter, UploadFile
bulk.go              # Send.Bulk, BulkIdempotencyKey
mask.go              # String 마스킹 (MaskPhone)
errors.go errors_json.go validate.go        # 오류 타입과 종류, JSON 직렬화, 검증기(조건부 필수 평가기 `requiredIf` 포함)
send.go files.go reports.go messages.go webhook.go   # 직접 작성한 P0 편의 메서드와 웹훅
bizgotest/           # 사용자용 테스트 도구: Fake(RoundTripper), 스텁·오류 주입, SignWebhook
otel/                # 별도 모듈: hooks 위의 OpenTelemetry 어댑터 (core는 의존성 없음 유지)
internal/testserver/ # 테스트·예제용 mock 서버
examples/*/          # 예제 프로그램. 각 main_test.go가 mock으로 실행
```

## 규칙

1. **생성 파일(`*_gen.go`, `operations_gen_test.go`, `testdata/spec.json`)은 손으로 고치지 않습니다.** 모델·메서드를 바꾸려면 bizgo-api-spec을 고치고 `spec/`에 복사한 뒤 재생성합니다. 생성기가 모르는 스키마 키워드·파라미터 형식·x-format은 오류로 멈춥니다(제약이 조용히 빠지지 않게). 생성기를 고쳐서 지원하세요.
2. 새 operation은 스펙에 `x-sdk-resource`/`x-sdk-method`/`x-sdk-retry`(+ `x-sdk-pagination`, `x-sdk-result`, `x-sdk-rate`)를 달면 메서드가 생성됩니다. `client.<Resource>.<Method>`, 인자는 경로 파라미터 → 본문 → `<OperationID>Params`.
3. 직접 작성한 편의 메서드가 생성될 메서드와 같은 타입·이름이면 **편의 메서드가 이기고** 생성하지 않습니다(생성기가 패키지 소스를 읽어 판단). 그런 operation은 `spec_test.go`의 `TestHandwrittenOperations`와 `operations_test.go`의 `handwrittenCalls`에 넣습니다. 목록 operation이면 `Iter<Method>`도 직접 작성해야 합니다.
4. 모든 공개 메서드는 `context.Context`를 첫 인자로 받고, 문서 주석을 갖추며, 생성 모델이나 `types.go`의 타입을 반환합니다(`map` 반환 금지). 요청에는 반드시 `op`(operation 메타데이터)를 넣어 hooks·속도 제한이 동작하게 합니다.
5. 재시도 정책은 `x-sdk-retry`를 따릅니다. 예외: `Send.*`는 `IdempotencyKey`가 있으면 `retrySafe`. 3xx는 재시도하지 않습니다.
5a. `idempotencyKey`가 있고 `idempotencyTtl`이 없으면 `DefaultIdempotencyTTL`(86400)을 채워 보냅니다(없으면 A309, sandbox 2026-09-28). 직접 지정한 값(0 포함)은 그대로, 키가 없으면 TTL을 더하지 않습니다. 규칙은 `runtime.go`의 `defaultIdempotencyTTL` 한 곳에 있고, 생성기가 두 필드를 가진 요청 본문 모델마다 `withDefaultIdempotencyTTL()`(복사본에 채움, 입력 불변)을 만들며 생성 operation과 `Send.Request`(→ `Send.*`, `Send.Bulk` 청크)가 이를 호출합니다. 새 발송 경로도 이 메서드를 거쳐야 합니다(테스트: `send_test.go`, `convenience_test.go`, `tools/gen/idempotency_test.go`).
5b. 같은 `idempotencyKey`로 다시 보내면 요청은 성공(HTTP 200)하고 수신자별 `destinations[].code`가 A301입니다(sandbox 2026-09-28, SDK-DESIGN §12 4번). 이 수신자는 `SendResult.Duplicates()`/`BulkSendResult.Duplicates()`로 돌려주고 `Failed()`·`Succeeded()`에 넣지 않습니다(코드 상수는 `types.go`의 `destinationAccepted`/`destinationDuplicate`, 수신자는 셋 중 정확히 하나). 수신자가 모두 A301인 청크도 오류가 아닙니다. 요청 단위 A301(`data.code`)은 그대로 `ErrDuplicateRequest`입니다. 테스트: `send_test.go`의 `TestPerRecipientA301IsDuplicateNotFailed`, `convenience_test.go`의 `TestBulkAggregatesDuplicates`.
6. 속도 제한 send 버킷은 스펙의 `x-sdk-rate: send`인 operation만 씁니다(SDK에 목록을 하드코딩하지 않음). 비용은 본문의 `destinations` 개수(최소 1).
7. 보내기 전 검증 오류는 `*ValidationError`로 돌려줍니다(panic 금지). 파일 오류도 검증 오류이며 파일 이름만 씁니다.
7a. 조건부 필수(`x-sdk-required-if`, bizgo-api-spec AGENTS.md 11번·SDK-DESIGN §4)는 SDK에 하드코딩하지 않습니다. 생성기(`tools/gen/requiredif.go`)가 규칙을 스키마와 대조해 검사하고(모르는 키·연산자, 없는 속성·경로는 오류), 요청 본문 스키마마다 규칙이 붙은 객체의 위치 표(`requiredIf<모델>`, 예: `messageFlow[].alimtalk`)를 만듭니다. 루트 모델의 생성된 `validate()`가 필드 검사 뒤 `validator.requiredIf`(`validate.go`)를 호출하며, 이 평가기는 본문의 JSON 형태(실제로 보내는 바이트)에서 규칙을 적용하므로 `$.` 경로(`$.destinations[].replaceWords`)는 요청 루트 기준으로 검사됩니다. 첫 속성이 루트에 없는 `$.` 경로는 그 루트에서 건너뜁니다. 오류는 경로와 스펙의 조건만 씁니다(입력값 금지). 테스트: `requiredif_test.go`, `tools/gen/requiredif_test.go`.
8. 예제·README의 코드는 실제로 동작해야 합니다. README의 Go 블록은 `readme_test.go`가 컴파일합니다(블록에서 쓰는 이름은 그 prelude에 있음). 예제를 바꾸면 그 `main_test.go`도 맞춥니다.
9. core 모듈의 런타임 의존성은 두지 않습니다(표준 라이브러리만, CI가 `go list -m all`로 검사). 선택 기능의 의존성은 `otel/`처럼 별도 모듈에 둡니다.
10. 사용자 콜백(hooks, 로거, 웹훅 처리 함수, 대량 발송 청크)의 panic은 recover해서 결과에 영향을 주지 않게 합니다.
10a. 성공 응답에서는 `nil` 결과를 돌려주지 않습니다(빈 구조체·빈 슬라이스, SDK-DESIGN §12.19). 새 파라미터 구조체에 전화번호·내용 필드가 있으면 `String`/`GoString` 마스킹을 붙입니다(생성 코드는 생성기가, 직접 작성한 구조체는 `params_string.go`).
11. 문서에 없는 동작을 가정하지 않습니다. 불확실하면 스펙에 `x-unverified`로 표시하고 bizgo-api-spec의 GitHub Issues에 알립니다.
12. GitHub Actions는 검증된 커밋 SHA와 버전 주석으로 고정합니다. 새 action의 SHA는 추측하지 말고 확인된 값을 받아 씁니다.
13. 버전은 `version.go`의 `Version`(현재 `1.2.0`, 비즈고 SDK 6종 공통)입니다. `User-Agent`/`X-Bizgo-Client`에 쓰이고 릴리스 태그 `v<Version>`과 같아야 합니다(`release.yml`이 검사). 올릴 때는 `spec_test.go`·`readme_test.go`, `otel/go.mod`의 require, README·llms.txt·SECURITY.md·CHANGELOG.md를 함께 바꿉니다.

## 보안 규칙 (오픈소스 저장소)

- **로그·오류에 민감정보 금지**: API Key, 요청 본문, 쿼리 문자열, 전화번호, 웹훅 secret을 로그, 오류 메시지, hook 이벤트, `String`/`GoString`에 넣지 않습니다. 모델의 `String`은 생성기가 전화번호를 가리고 내용은 길이만 보이게 만듭니다(`tools/gen/models.go`의 `maskCall`: `phoneFields`/`personFields`/`secretFields`/`contentFields`, 모델·파라미터 공통, `String`/`GoString`/`LogValue`). net/http의 `*url.Error`는 전체 URL을 담으므로 `stripURL`로 벗겨 냅니다. 관련 테스트: `errors_test.go`, `hardening_test.go`.
- 검증 오류에는 필드 경로와 이유만 씁니다(입력값 금지).
- 사용자 HTTP 클라이언트는 SDK가 점검할 수 있을 때만 받습니다(`checkHTTPClient`). 점검할 수 없는 RoundTripper는 `WithTrustedHTTPClient` 명시가 필요합니다. 프록시·CA는 `WithProxy`/`WithRootCAs`로 제공합니다.
- TLS 검증을 끄는 옵션, `http://` base URL(localhost 제외), 리다이렉트 추종을 추가하지 않습니다. 사용자 URL로 Authorization 헤더를 보내는 기능을 만들지 않습니다. SDK 헤더(`Authorization`, `User-Agent`, `X-Bizgo-Client`)는 사용자 옵션으로 바꿀 수 없어야 합니다.
- 경로 파라미터는 `segment()`/`buildPath()`로 인코딩합니다(`.`/`..`/빈 값 거부).
- 리포트·MO 웹훅 서명은 `X-IB-Signature` = `HmacSHA256(secret, X-IB-Timestamp)`입니다(비즈고 확인). 다른 서명 헤더 이름이나 `sha256=` 같은 접두어는 받지 않고, 출력 인코딩은 아직 확인되지 않아 hex(대소문자 무시)·base64를 모두 허용합니다. 웹훅 secret은 비즈고에 요청해 받습니다.
- 리포트·MO 웹훅은 항상 서명이 필요합니다. 상담톡 웹훅에는 서명이 없습니다(서명은 리포트·MO 웹훅에만 적용): 생성기는 스펙에 `X-IB-Signature` 헤더가 없는 웹훅을 검증 없는 파서로 만들고, 서명 헤더가 와도 무시합니다. 본문 검사(1MB, JSON 깊이 64, 타입)는 그대로 적용합니다. 상담톡만 받는 코드는 secret 없이 `ParseCounsel*Webhook`을 씁니다.
- 테스트·예제의 값은 placeholder만 씁니다: 전화번호 `01000000000`, `01000001234`, 키 `test-api-key-not-real`, `SENDER_KEY_EXAMPLE`, `USER_KEY_EXAMPLE`. 실제 키·번호·발신프로필 키를 커밋하지 않습니다(gitleaks가 pre-commit·CI에서 검사).
