# 보안 정책

## 취약점 신고

보안 취약점은 **공개 Issue로 올리지 마세요.** GitHub의 [Private vulnerability reporting](../../security/advisories/new)으로 비공개 신고해 주세요.
확인 후 영업일 기준 5일 안에 답변합니다.

지원 버전: 최신 minor 버전에 보안 수정을 제공합니다.

| 버전 | 지원 |
|------|------|
| 1.2.x (`v1.2.*`, `otel/v1.2.*`) | 예 |
| 1.2.0 이전 | 공개 배포되지 않음 |

## 자격 증명이 노출됐을 때

1. 즉시 비즈고 콘솔 `발송관리 > 연동관리`에서 해당 API Key를 폐기하고 새로 발급합니다. 저장소 기록에서 지워도 이미 복제됐을 수 있으므로 **폐기가 먼저**입니다.
2. 웹훅 secret은 비즈고에 재발급을 요청합니다.
3. 허용 IP(ACL) 목록을 점검합니다.

## SDK의 보안 동작

- API Key는 `Authorization` 헤더로만 보내고, `String`/`GoString`·로그·오류 메시지·hook 이벤트에 넣지 않습니다. 키에 공백·줄바꿈·비ASCII 문자가 있으면 조용히 잘라내지 않고 설정 오류로 알립니다.
- `Authorization`, `User-Agent`, `X-Bizgo-Client`, `Accept`, `Content-Type`은 SDK가 요청마다 마지막에 설정합니다. 스펙의 헤더 파라미터가 이 헤더를 쓸 수 없고(생성기가 거부), 헤더 파라미터 값에 줄바꿈·제어문자가 있으면 보내기 전에 거부합니다.
- 요청 본문, 쿼리 문자열, 검증 오류의 입력값(전화번호 포함 가능)을 로그·오류에 넣지 않습니다. net/http의 `*url.Error`(전체 URL 포함)는 오류 체인에서 제거합니다. 대량 발송 오류에는 수신자 인덱스 범위만 있습니다.
- 모델과 파라미터 구조체(직접 작성한 `SMSParams`·`LMSParams`·`MMSParams`·`OmniParams`·`BulkParams`·`MOHistoryParams`·`UploadParams`와 생성된 모든 `<OperationID>Params`)의 `String`/`GoString`(`%v`, `%+v`, `%#v`)은 스펙의 전화번호 필드(`to`, `from`, `phoneNumber`, `phoneNumbers`, `originCID`, `callback`, `mdn` 등)를 가리고(11자리 이상 `010****0000`, 8~10자리 절반 이상, 7자리 이하 전부), 메시지 내용·토큰은 길이만, 수신자·메시지 목록은 개수만 보입니다. 사람 이름·이메일은 첫 글자만, 토큰·인증번호·암호화된 인증정보는 `[REDACTED]`, `replaceWords`는 개수만 보입니다. `log/slog`는 `LogValue`로 같은 값을 남깁니다. 필드 이름으로 판단하므로 그 밖의 필드(`ref` 등)에 넣은 개인정보는 가리지 않습니다. `Client`는 `%v`로 출력해도 base URL만 보입니다.
- 로그는 `WithLogger`로 로거를 줄 때만 남기며, `METHOD path -> status (ms, attempt n)` 한 줄뿐입니다. hook 이벤트에는 경로 템플릿만 있고 실제 경로 값·쿼리·본문은 없습니다. 로거·hook의 panic은 결과에 영향을 주지 않습니다.
- `APIError.Body`와 `InvalidResponseError.Body`에는 응답 원문이 있습니다. 전화번호가 있을 수 있으니 그대로 로그에 남기지 마세요(오류를 JSON으로 직렬화할 때도 포함됩니다).
- https가 아닌 base URL은 거부합니다(테스트용 localhost 제외). base URL의 사용자 정보·쿼리·fragment도 거부합니다.
- **TLS**: 인증서 검증을 끄는 방법은 없습니다. 기본 클라이언트는 TLS 1.2 이상, 시스템 인증서 저장소(또는 `WithRootCAs`/`WithRootCAsFile`로 준 CA)로 검증합니다. 프록시는 `WithProxy`로 설정합니다.
- **사용자 HTTP 클라이언트**(§12.6): `WithHTTPClient`는 SDK가 점검할 수 있는 클라이언트만 받습니다(Transport 없음 또는 `InsecureSkipVerify`가 꺼진 `*http.Transport`, bizgotest fake). 쿠키 Jar나 CheckRedirect가 있거나 점검할 수 없는 RoundTripper면 설정 오류입니다. 점검할 수 없는 RoundTripper는 `WithTrustedHTTPClient`로 명시적으로 허용해야 하며, 이때 그 RoundTripper가 리다이렉트를 따르거나 재시도하거나 인증을 바꾸지 않는다는 것은 호출자가 보장합니다. 재시도하면 메시지가 중복 발송될 수 있고, 요청을 기록·전달하면 API Key와 전화번호가 노출됩니다.
- 리다이렉트를 따르지 않습니다(오류 메시지에 HTTP 상태 포함, 재시도하지 않음). 사용자 클라이언트도 복사한 뒤 리다이렉트를 막으므로 API Key가 다른 주소로 가지 않습니다.
- 응답 본문은 압축 해제 후 16MB, 웹훅 본문은 1MB, JSON 깊이는 64단계까지만 받습니다. 경로 파라미터는 한 세그먼트로 인코딩하고 `.`/`..`/빈 값은 거부합니다. `Retry-After`는 0~60초의 유한한 숫자만 따릅니다.
- 발송·생성 요청은 멱등성이 보장되지 않으면 타임아웃·5xx 후 자동 재시도하지 않습니다(중복 발송 방지). 재시도한 요청이 요청 단위 `A301`을 받으면 `AlreadyAccepted`로 알립니다. 같은 키로 다시 보낸 요청에서 이미 접수된 수신자(수신자별 A301)는 `Duplicates()`로 돌아오며 다시 발송되지 않습니다.
- 웹훅 서명은 `hmac.Equal`로 constant-time 비교하고 timestamp 허용 오차(기본 5분, 1~16자리 숫자만)를 벗어난 오래된 요청을 거부합니다.
  운영 환경에서는 HTTPS, 발신 IP 허용 목록, `msgKey` 기준 중복 제거를 함께 적용하고, 중요한 판단은 조회 API로 결과를 확인하세요.
- 리포트·MO 웹훅은 항상 서명이 필요합니다.
- **상담톡 웹훅**: 상담톡 웹훅에는 서명이 없습니다(서명은 리포트·MO 웹훅에만 적용). SDK는 상담톡 웹훅의 서명을 요구하거나 검사하지 않고(서명 헤더가 와도 무시), 본문 크기(1MB)·JSON 깊이(64단계)·타입 검사만 적용합니다. 상담톡만 받는다면 webhook secret 없이 `bizgo.ParseCounsel*Webhook`을 쓸 수 있습니다. 상담톡 엔드포인트에도 HTTPS, 비즈고 웹훅 발신 IP 허용 목록, `msgKey` 기준 중복 제거를 적용하세요. 상담톡 본문(상담 내용, 전화번호·닉네임, 암호화된 본인인증 정보)은 개인정보로 다루고 로그에 남기지 마세요.
- 응답 봉투(최상위·`common`·`data`)에 같은 키가 두 번 있으면 해석하지 않고 `InvalidResponseError`로 거부합니다(마지막 값으로 실패가 성공으로 바뀌는 것을 막음).
- 웹훅 핸들러는 본문 크기(1MB)만 제한합니다. 느린 요청은 `http.Server`의 `ReadHeaderTimeout`/`ReadTimeout`으로 끊으세요(README 예제 참고).
- 클라이언트 속도 제한은 한 프로세스 안에서만 동작합니다. 한도는 계정 단위이므로 여러 서버가 같은 키를 쓰면 따로 조율해야 합니다.

## 공급망

- 런타임 의존성이 없습니다(Go 표준 라이브러리만). 코드 생성기(`tools/gen`)와 OpenTelemetry 어댑터(`otel/`)는 별도 모듈이며, 어댑터를 쓰지 않는 사용자에게 그 의존성은 전달되지 않습니다.
- Go 모듈은 git 태그로 배포되며 저장소에 배포 토큰을 두지 않습니다. 모듈 내용은 Go 체크섬 데이터베이스(sum.golang.org)로 검증됩니다.
- 모든 커밋과 PR은 gitleaks로 검사하고, 코드와 의존성은 govulncheck로 검사합니다. GitHub Actions는 커밋 SHA로 고정합니다.
