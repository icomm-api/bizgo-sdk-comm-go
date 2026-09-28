# 예제

모든 예제는 **sandbox**(실제 발송 없음)에 연결하고, 값은 환경변수에서 읽습니다.

```bash
export BIZGO_API_KEY=...            # 콘솔 > 발송관리 > 연동관리 (코드에 쓰지 마세요)
export BIZGO_FROM=...               # 등록한 발신번호
export BIZGO_TO=...                 # 테스트 수신번호
go run ./examples/send-sms
```

| 디렉터리 | 내용 |
|---|---|
| `send-sms` | SMS 발송과 수신자별 접수 결과 확인 |
| `alimtalk-fallback` | 알림톡 발송, 실패 시 SMS 대체발송, 멱등성 키, 중복 요청 처리 (`BIZGO_KAKAO_SENDER_KEY`, `BIZGO_KAKAO_TEMPLATE_CODE`) |
| `send-mms` | 이미지 업로드 후 MMS 발송 (`BIZGO_IMAGE`) |
| `poll-reports` | 리포트 Polling 처리(처리 성공 시에만 수신 확인) |
| `message-history` | 발송 이력 전체 조회(페이지 자동 순회)와 상태 조회 |
| `webhook-server` | 리포트 웹훅 수신 서버(서명 검증, 중복 처리, 5초 안에 응답) (`BIZGO_WEBHOOK_SECRET`) |
| `bulk-send` | 수신자 목록 대량 발송(200명씩 청크, 멱등성 키로 재실행 안전, 청크별 오류) (`BIZGO_TO`는 쉼표로 구분) |
| `alimtalk-templates` | 스펙에서 생성된 리소스 사용: 승인된 알림톡 템플릿 전체 순회 (`BIZGO_KAKAO_SENDER_KEY`) |

각 예제의 `run` 함수는 `main_test.go`에서 mock 서버나 `bizgotest`로 실행되므로(`go test ./...`), 예제 코드는 항상 현재 SDK와 맞습니다.
