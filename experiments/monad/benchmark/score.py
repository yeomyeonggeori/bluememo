import json, sys, urllib.request

SCHEMA = {"type":"object","properties":{"propositions":{"type":"array","items":{"type":"object",
  "properties":{"content":{"type":"string"},
                "kind":{"type":"string","enum":["identity","preference","fact","episode","procedure","unknown"]},
                "validUntil":{"type":"string"}},
  "required":["content","kind","validUntil"]}}},"required":["propositions"]}

CONTEXT_KO = "\n\n맥락\n화자: 이동하 (여명거리 CTO)\n오늘: 2026-09-21. 이번 분기는 2026-07-01부터 2026-09-30까지."
CONTEXT_EN = "\n\nContext\nSpeaker: 이동하 (CTO of 여명거리)\nToday: 2026-09-21. The current quarter runs 2026-07-01 to 2026-09-30."

CASES = [
  {"input": "내 이름은 이동하고, 나이는 14살이야. 사과를 좋아해.",
   "checks": [("이름 보존",      lambda ps: any("이동하" in p["content"] for p in ps) and not any("이동하고" in p["content"] or "이동은" in p["content"] for p in ps)),
              ("나이 명제",      lambda ps: any("14" in p["content"] for p in ps)),
              ("선호 분류",      lambda ps: any(p["kind"]=="preference" and ("사과" in p["content"] or "apple" in p["content"].lower()) for p in ps)),
              ("동어반복 없음",  lambda ps: not any(p["content"].count("이동하")>=2 and ("이름" in p["content"] or "name" in p["content"].lower()) for p in ps))]},
  {"input": "김여명이 그러는데 박예시는 아침에만 커피를 마신대. 이번 분기만 나한테 한국어로 답해줘.",
   "checks": [("출처 한 명제",  lambda ps: any("김여명" in p["content"] and "박예시" in p["content"] for p in ps)),
              ("출처 안 쪼갬",  lambda ps: not any("김여명" in p["content"] and "박예시" not in p["content"] and len(p["content"])<30 for p in ps)),
              ("조건 보존",      lambda ps: any(("아침" in p["content"] or "morning" in p["content"].lower()) for p in ps)),
              ("만료일 정확",    lambda ps: any(p["validUntil"]=="2026-09-30" for p in ps)),
              ("환각 없음",      lambda ps: not any("회의록" in p["content"] or "meeting note" in p["content"].lower() for p in ps))]},
  {"input": "릴리스는 admind와 capabilityd를 함께 올린다. 빌드 전에 현재 브랜치가 origin/main을 포함하는지 확인한다.",
   "checks": [("식별자 보존",    lambda ps: any("admind" in p["content"] for p in ps) and any("origin/main" in p["content"] for p in ps)),
              ("절차 분류",      lambda ps: sum(1 for p in ps if p["kind"]=="procedure")>=1),
              ("맥락 누출 없음", lambda ps: not any(p["content"].strip() in ("이동하","2026-09-21") for p in ps))]},
]

def decompose(model, system, text):
    body = json.dumps({"model":model,"stream":False,"think":False,"options":{"temperature":0},
        "messages":[{"role":"system","content":system},{"role":"user","content":text}],
        "format":SCHEMA}).encode()
    request = urllib.request.Request("http://127.0.0.1:11434/api/chat", body, {"Content-Type":"application/json"})
    with urllib.request.urlopen(request, timeout=900) as response:
        return json.loads(json.loads(response.read())["message"]["content"])["propositions"]

models = ["qwen3.5:2b"]
langs = [("한국어 출력", open(f"{sys.argv[1]}/prompt.txt").read(), CONTEXT_KO),
         ("영어 출력",   open(f"{sys.argv[1]}/en-prompt.txt").read(), CONTEXT_EN)]

print(f"{'모델':<14}{'출력':<12}{'점수':>8}   실패한 판정")
print("─"*78)
results = {}
for model in models:
    for label, instruction, context in langs:
        passed, total, failures = 0, 0, []
        for case in CASES:
            try:
                props = decompose(model, instruction+context, case["input"])
            except Exception as error:
                props = []
                failures.append(f"호출실패({type(error).__name__})")
            for name, check in case["checks"]:
                total += 1
                if not props:
                    failures.append(name)
                    continue
                try:
                    ok = check(props)
                except Exception:
                    ok = False
                if ok: passed += 1
                else: failures.append(name)
        results[(model,label)] = (passed,total)
        print(f"{model:<14}{label:<12}{passed:>4}/{total:<3}   {', '.join(failures) if failures else '—'}")
