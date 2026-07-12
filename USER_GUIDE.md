# NA Room — User Guide

**Version:** June 2026  
**Language:** English (translate to RU / ES / KA before publishing)

---

## What is NA Room?

NA Room is an anonymous platform where people struggling with addiction can connect with peers who have lived experience and want to help.

**What it is:**
- Peer-to-peer support, not professional care
- Anonymous by design — no accounts, no identity
- End-to-end encrypted chat
- Accessible on regular browsers and Tor

**What it is not:**
- Not a medical service
- Not a crisis hotline
- Not therapy or treatment
- Not a substitute for emergency services

> ⚠ If you are in immediate danger, please contact your local emergency services.

---

## How the platform works — overview

```
Person posts a listing (anonymous, $5, 24 hours)
        ↓
Peers browse the board and respond (free)
        ↓
Person reviews responses and accepts one
        ↓
Peer pays $15 → private encrypted chat opens
        ↓
Session runs up to 24 hours, then auto-closes
        ↓
Optional anonymous feedback (👍 / 👎)
```

No names. No phone numbers. No email. The only identifier is a crypto wallet address used to verify that both sides are real people — no funds are linked to identity.

---

## Language

The platform detects your device language automatically on first visit and shows the interface in your language if it is supported (English, Russian, Spanish, Georgian).

**Priority order:**
1. Your previously saved preference (stored locally in your browser)
2. Your device/browser language
3. English — used as default when no language can be detected (e.g. Tor Browser, private browsing with no language headers)

You can switch the language manually at any time using the selector in the bottom-right corner of every page. Your choice is saved in your browser and remembered on return visits.

---

## For the person seeking help (client)

### Step 1 — Go to the board

Open NA Room. You will land on the board for your city. If the city is wrong, switch it using the tabs at the top.

### Step 2 — Post a listing

Tap **"I need help"** on the board.

You will choose:
- **City** — where you are
- **What you are dealing with** — type of substance or behaviour (alcohol, opioids, cannabis, gambling, etc.)
- **What kind of help** — crisis support, relapse prevention, withdrawal support, motivation, just talk, recovery plan
- **Urgency** — urgent / soon / can wait
- **Languages** — what languages you can speak

No free text is entered at this stage. Everything is selected from fixed options — this protects your anonymity.

### Step 3 — Verify your wallet

You need a Bitcoin (BTC) or Litecoin (LTC) wallet with a minimum balance of **$150**.

This is not a payment — no funds are moved. The balance check is used only to confirm you are a real person, not an automated bot. You enter your wallet address; the platform checks the on-chain balance.

> Your wallet address is not stored beyond the session. It is used only to verify balance and to let you access your listing later.

### Step 4 — Pay $5 to publish

Send exactly the amount shown (in BTC or LTC) to the address on screen. The page checks automatically every few seconds.

After 1 blockchain confirmation your listing appears on the board and stays visible for **24 hours**.

You can leave — you do not need to stay online.

### Step 5 — Review responses

Return to your listing (use the wallet address you posted with to access it). You will see a list of peers who responded.

For each peer you can see:
- Number of completed sessions
- Percentage of positive feedback
- How long they have been on the platform

No names. No personal details. Peers are shown as Peer #1, Peer #2, etc.

### Step 6 — Accept one peer

Choose the peer you want to talk to and tap **Accept**.

This creates a $15 invoice for the peer to pay. You do not pay anything at this step.

If the peer pays within the listing's active window, the chat opens automatically.

### Step 7 — Chat

The chat is end-to-end encrypted. Messages are unreadable to anyone except you and your peer, including the platform.

- The session runs for up to **24 hours**
- Either side can end the session at any time
- When the session ends, **all messages are permanently deleted**
- No logs are kept

If the peer you chose does not pay, you can go back and accept a different peer. If your listing expires, you can renew it for free (adds 24 hours) as long as fewer than 2 paid chats have opened on it — there is no 30-day cutoff.

### Step 8 — Leave feedback (optional)

After the session ends you can leave anonymous feedback: 👍 or 👎. This helps others on the platform find reliable peers over time.

---

## For peers

Peers are people with lived experience who want to support others going through similar struggles. You do not need any professional credentials — the platform is peer support, not professional care.

### Who can respond as a peer?

Anyone who wants to help and can verify a BTC or LTC wallet with **$1,000+ balance**.

The balance requirement exists to create accountability without exposing identity. Your wallet is not linked to your real name or any personal information.

**Limit:** $1,000 balance gives you 2 active response slots; each additional $1,000 adds 2 more. For example:
- $1,000 balance → 2 active responses
- $2,000 balance → 4 active responses
- $3,000 balance → 6 active responses

Your funds are never moved — the balance is checked, not touched.

### Step 1 — Browse the board

Go to the board for your city. You do not need to create an account. Browse freely.

Listings show:
- Type of issue (substance or behaviour)
- Type of help requested
- Urgency level
- Languages the person speaks
- Time remaining on the listing

### Step 2 — Respond to a listing

Open a listing and tap **"I can help with this"**.

Enter your BTC or LTC wallet address. The platform verifies your balance. If you meet the requirement and have an open response slot, your response is sent — free of charge at this step.

You can respond to multiple listings (within your slot limit). You can cancel a response before the client accepts, with a 30-minute cooldown before responding to the same listing again.

### Step 3 — Wait for acceptance

You will be notified if the client accepts your response. At that point you will see a payment screen.

### Step 4 — Pay $15 to open chat

Send exactly the amount shown to open the encrypted chat. After 1 confirmation the chat opens on both sides.

The $15 ensures that only peers who are genuinely committed reach the conversation.

### Step 5 — Chat

The same rules apply as for the client:
- End-to-end encrypted
- Up to 24 hours
- Either side can close at any time
- All messages permanently deleted on close

You can close the session yourself at any time. The client may also close the session and move to a different peer.

### Step 6 — Reputation

After each session the client can leave 👍 or 👎. Over time your session count and positive feedback percentage build your reputation on the platform — anonymously, without revealing who you are.

---

## Privacy and anonymity

- **No accounts** — the wallet address is the only identifier, and it is not linked to identity
- **No email, no phone, no username**
- **No message storage** — all chat messages are deleted when the session ends
- **No IP logging** beyond what is necessary for platform operation
- **Tor-compatible** — the platform works on .onion address for users who need additional anonymity
- **Open source** — full source code is available at https://github.com/naroomer/naroom; anyone can verify encryption, data deletion, and the absence of hidden functions
- **Language fallback** — Tor Browser and browsers with no language headers open in English by default, with no fingerprinting attempt

---

## What the platform does not do

- Does not provide medical advice
- Does not diagnose or treat any condition
- Does not verify peer qualifications or credentials
- Does not retain conversation content
- Does not facilitate the purchase or sale of any substance
- Does not know the real identity of any user

---

## Emergency

NA Room is not an emergency service.

If you or someone around you is in immediate danger, please contact your local emergency services:

- Georgia: **112**
- Russia: **112**
- Argentina / Brazil: **112** or local equivalent
- Vietnam: **113** (police) / **115** (ambulance)

---

*NA Room — peer support, not professional care.*
