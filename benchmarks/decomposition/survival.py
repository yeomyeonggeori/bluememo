import json, re, sqlite3, sys, collections

STOP = set('the a an of and to in on is was were be been at for with that this it he she they i you my his her their our as by from or not no yes do does did has have had will would can could what when where who how why there here them us me we so if then than too very just more most some any all each'.split())

def words(text):
    return set(re.findall(r'[a-z0-9]+', str(text).lower())) - STOP

def survival(store_path, dataset_path, unit):
    store = sqlite3.connect(store_path)
    facts = words(' '.join(r[0] for r in store.execute('select content from memory')))
    notes = words(' '.join(r[0] for r in store.execute('select body from pending_note')))
    data = json.load(open(dataset_path))
    counts = collections.Counter()
    lost = []
    for sample in data:
        if sample.get('sample_id') != unit:
            continue
        for question in sample.get('qa', []):
            gold = words(question.get('answer') or question.get('adversarial_answer'))
            if not gold:
                continue
            in_notes = len(gold & notes) / len(gold)
            if in_notes < 0.7:
                counts['not in the conversation'] += 1
                continue
            counts['reached the facts' if len(gold & facts) / len(gold) >= 0.7 else 'lost by decomposition'] += 1
            if len(gold & facts) / len(gold) < 0.7:
                lost.append((question.get('question', '')[:60], str(question.get('answer'))[:40]))
    return counts, lost

counts, lost = survival(sys.argv[1], sys.argv[2], sys.argv[3])
kept = counts['reached the facts']
dropped = counts['lost by decomposition']
total = kept + dropped
print(f"{sys.argv[1].split('/')[-2]}: {kept}/{total} survived = {kept/max(total,1):.3f}  (outside the conversation: {counts['not in the conversation']})")
for q, g in lost[:5]:
    print(f"   lost  Q: {q}  gold: {g}")
