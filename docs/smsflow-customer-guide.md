# Set up SMSFlow SMS and WhatsApp fallback

This guide applies to the channel-enabled Whatomate release. Ask your hosting
administrator whether that release is available on your instance. SMSFlow SMS
uses your own SMSFlow account and balance. Your Whatomate subscription does not
include SMS credits.

## Connect your account

1. Obtain a dedicated Client ID and Client Secret from your SMSFlow account with
   SMPP integration access. Use a separate credential for each workspace. Keep
   the secret out of chat messages, screenshots and support tickets.
2. Sign in as an administrator and open **Settings → Channels**. If you cannot
   manage connections, ask an administrator for access; agents can send through
   permitted connections without seeing their credentials.
3. Give the connection a recognizable name, enter the Client ID and Client
   secret, and keep **Maximum segments per message** at three to start.
4. Choose **Save connection**. Validation checks your registration and balance
   without sending an SMS. Check the resulting connection state and any action
   needed message before sending. If the secret is incorrect, correct it and
   revalidate the saved connection with its original Client ID. A saved Client
   ID cannot be changed, even if it was mistyped before validation; ask an
   administrator to replace that connection with the correct one. After a
   disabled connection validates successfully, deliberately choose **Enable
   dispatch** when you are ready to send.
5. Check whether it says **Replies enabled** or **No inbound route**. One-way
   accounts can send messages. Replies and reply bots require a verified
   reply-capable SMSFlow route; entering a credential alone cannot enable them.

The host controls the trusted SMSFlow endpoint. There is no customer field for
an arbitrary API URL. A host encryption warning requires your hosting
administrator's attention before a connection can be created.

## Send a first message

Use a contact with a valid international phone number and permission to receive
your SMS. A WhatsApp-only contact identifier is insufficient. Select **SMS** and
the intended connection in the inbox, author a plain-text message, confirm
recipient consent, and review the encoding and segment preview before sending.
Use an agreed test recipient for the first send; sending consumes SMSFlow credit.

The preview distinguishes GSM-7 and Unicode. Some GSM characters consume two
units, while emoji can consume more than one Unicode unit. Concatenated messages
have less space per segment: GSM-7 allows 160 units for one segment or 153 per
multipart segment; Unicode allows 70 or 67 UTF-16 units respectively. The final
preview is the useful check, especially after campaign personalization.
Content above the connection's limit is rejected; it is never silently cut off.
Administrators can change the limit from one to ten segments.

SMS campaigns use the same connection, consent and segment controls. Each
recipient's final personalized text must fit the limit. Use a small agreed
recipient group before starting a large campaign.

## Enable fallback deliberately

Fallback starts off. Opt in for the particular inbox message, campaign, bot
action or API request and select its SMS connection. Plain WhatsApp text can be
reused. For a template, image, document, button or other rich message, write a
separate SMS alternative. Include usable text instructions instead of assuming
WhatsApp buttons or media will appear in the SMS.

A supported confirmed recipient-delivery failure can trigger fallback.
Authentication, account configuration, consent/suppression and uncertain
submission errors do not. If you also enable the optional undelivered timeout,
its default is ten minutes. The timeout concerns delivery, not whether someone
has read a message. A delivered-but-unread WhatsApp message does not trigger SMS.

Whatomate keeps WhatsApp and SMS delivery attempts together in the conversation.
Confirmed WhatsApp delivery cancels a fallback that has not been dispatched.
Both messages may arrive if WhatsApp delivers after the SMS was submitted.
Never promise a customer that fallback can recall an already submitted SMS.

## Understand delivery and account messages

| What you see | What it means and what to do |
| --- | --- |
| Queued or waiting | Work has been saved. Check connection state before creating another message. |
| Submitted | SMSFlow accepted the submission; this is not handset delivery. |
| Delivered | A delivery report confirmed delivery. SMS has no read receipt. |
| Failed | Inspect the recorded reason. Fix the cause before deciding whether a new send is appropriate. |
| Unknown | The submission may have reached the provider. Wait for a delivery report and ask support to reconcile it. Do not create a replacement just because the first response was lost. |
| Low balance | Review or top up your SMSFlow account. The displayed unit is the provider's credits or currency; it is not an estimated Whatomate charge. |
| Action needed / polling failure | Reports or account checks may be delayed. Already submitted messages are retained. Ask an administrator to check the connection rather than resending. |

Delivery reports are normally polled every 30 seconds, with backoff during
failures. This interval is not a delivery-time guarantee. An SMS reply does not
open or extend WhatsApp's separate 24-hour messaging window.

## Stop messages, manage opt-outs and rotate access

In **Settings → Channels**, **Stop pending SMS dispatch** cancels unclaimed
queued work and pending SMS bot jobs for that connection. It cannot recall an
SMS already being submitted or accepted. Enabling dispatch later does not
restore cancelled messages. Review a stopped campaign before starting new work.

Respect opt-outs on every sending workflow. On a verified two-way connection,
STOP replies suppress further SMS. A one-way connection cannot receive an
unsubscribe reply; give recipients an appropriate alternative contact method
and record opt-outs before sending again. Only promise reply support when the
connection reports that it is available.

To rotate credentials, choose **Edit / rotate credentials**. Leaving both
credential fields empty retains the stored values. Editing retains the same
Client ID and registered SMSFlow account; it can replace the secret, not switch
the connection to a different Client ID. If a newly issued credential has a
different Client ID, ask your administrator to arrange a replacement connection
and reconcile pending work first. Do not use rotation to transfer a connection
to another customer. Coordinate validation and revocation with the administrator;
uncertain sends must not be copied into the replacement as new messages.

For support, provide the workspace, connection name, logical message ID,
approximate send time and visible status/error. Share recipient information
only through your private support channel. Never include the Client Secret or
an access token. See the [technical channel reference](channels.md) for API
retry identifiers and integration details.
