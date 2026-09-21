import json, sys, urllib.request

SLOTS = ["who","when","where","what","how","why","attributedTo"]
props_schema = {"content":{"type":"string"},
  "kind":{"type":"string","enum":["identity","preference","fact","episode","procedure","unknown"]},
  "validUntil":{"type":"string"}}
for slot in SLOTS:
    props_schema[slot] = {"type":"string"}
SCHEMA = {"type":"object","properties":{"propositions":{"type":"array","items":{"type":"object",
  "properties":props_schema, "required":["content","kind","validUntil"]+SLOTS}}},"required":["propositions"]}

SLOT_RULES_KO = """
각 명제에 6하원칙 칸을 채운다. 원문이 말하지 않은 칸은 반드시 빈 문자열로 둔다.
비우는 것이 정상이다. 대부분의 명제는 두세 칸만 찬다. 추측해서 채우지 마라.
- who: 그 명제의 주체
- when: 시점 (원문 표현 그대로, 예: "아침에만", "2026-09-20")
- where: 장소
- what: 대상
- how: 방식이나 조건
- why: 이유
- attributedTo: 이 명제를 말한 사람이 화자가 아니면 그 이름. 출처를 별도 명제로 쪼개지 말고 이 칸에 적는다.
"""
SLOT_RULES_EN = """
Fill the 5W1H slots on each proposition. Leave a slot as "" whenever the source
does not state it. Empty is normal; most propositions fill only two or three.
Never guess a slot.
- who / when / where / what / how / why
- attributedTo: the name of whoever said this, if not the speaker. Never split the
  source into its own proposition; put it here.
"""

CONTEXT_KO = "\n\n맥락\n화자: 이동하 (여명거리 CTO)\n오늘: 2026-09-21. 이번 분기는 2026-07-01부터 2026-09-30까지."
CONTEXT_EN = "\n\nContext\nSpeaker: 이동하 (CTO of 여명거리)\nToday: 2026-09-21. The current quarter runs 2026-07-01 to 2026-09-30."

def slot(p, name): return (p.get(name) or "").strip()

CASES = [
  {"input": "내 이름은 이동하고, 나이는 14살이야. 사과를 좋아해.",
   "checks": [("이름 보존",     lambda ps: any("이동하" in p["content"] for p in ps) and not any("이동하고" in p["content"] or "이동은" in p["content"] for p in ps)),
              ("선호 분류",     lambda ps: any(p["kind"]=="preference" and ("사과" in p["content"] or "apple" in p["content"].lower()) for p in ps)),
              ("빈칸 유지",     lambda ps: all(slot(p,"where")=="" and slot(p,"why")=="" for p in ps))]},
  {"input": "김여명이 그러는데 박예시는 아침에만 커피를 마신대. 이번 분기만 나한테 한국어로 답해줘.",
   "checks": [("출처 슬롯",     lambda ps: any("김여명" in slot(p,"attributedTo") and "박예시" in p["content"] for p in ps)),
              ("출처 안 쪼갬",  lambda ps: not any("김여명" in p["content"] and "박예시" not in p["content"] and len(p["content"])<30 for p in ps)),
              ("조건 보존",     lambda ps: any(("아침" in p["content"]+slot(p,"when")+slot(p,"how")) or ("morning" in (p["content"]+slot(p,"when")+slot(p,"how")).lower()) for p in ps)),
              ("만료일 정확",   lambda ps: any(p["validUntil"]=="2026-09-30" for p in ps)),
              ("환각 없음",     lambda ps: not any("회의록" in p["content"] or "meeting note" in p["content"].lower() for p in ps))]},
  {"input": "릴리스는 admind와 capabilityd를 함께 올린다. 빌드 전에 현재 브랜치가 origin/main을 포함하는지 확인한다.",
   "checks": [("식별자 보존",   lambda ps: any("admind" in p["content"] for p in ps) and any("origin/main" in p["content"] for p in ps)),
              ("절차 분류",     lambda ps: sum(1 for p in ps if p["kind"]=="procedure")>=1),
              ("빈칸 유지2",    lambda ps: all(slot(p,"where")=="" and slot(p,"attributedTo")=="" for p in ps))]},
]

def decompose(model, system, text):
    body = json.dumps({"model":model,"stream":False,"think":False,"options":{"temperature":0},
        "messages":[{"role":"system","content":system},{"role":"user","content":text}],
        "format":SCHEMA}).encode()
    request = urllib.request.Request("http://127.0.0.1:11434/api/chat", body, {"Content-Type":"application/json"})
    with urllib.request.urlopen(request, timeout=900) as response:
        return json.loads(json.loads(response.read())["message"]["content"])["propositions"]

base = sys.argv[1]
langs = [("한국어", open(f"{base}/prompt.txt").read()+SLOT_RULES_KO, CONTEXT_KO),
         ("영어",   open(f"{base}/en-prompt.txt").read()+SLOT_RULES_EN, CONTEXT_EN)]

print(f"{'모델':<14}{'출력':<8}{'점수':>7}   실패")
print("─"*80)
for model in sys.argv[2:]:
    for label, instruction, context in langs:
        passed, total, failures, samples = 0, 0, [], []
        for case in CASES:
            try: props = decompose(model, instruction+context, case["input"])
            except Exception as error: props = []; failures.append(f"호출실패({type(error).__name__})")
            samples.append(props)
            for name, check in case["checks"]:
                total += 1
                if not props: failures.append(name); continue
                try: ok = check(props)
                except Exception: ok = False
                if ok: passed += 1
                else: failures.append(name)
        print(f"{model:<14}{label:<8}{passed:>3}/{total:<3}   {', '.join(failures) if failures else '—'}")
        if label == "한국어":
            for p in samples[1][:2]:
                filled = {s: slot(p,s) for s in SLOTS if slot(p,s)}
                print(f"      · {p['content'][:44]}  {filled}")
