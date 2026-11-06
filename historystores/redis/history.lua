local operation = ARGV[1]
local messages = KEYS[1]
local ledger = KEYS[2]
local closedField = 'closed'
local receiptPrefix = 'append:'
local accepted = 1
local conflict = -1
local closed = -2

local ledgerType = redis.call('TYPE', ledger).ok
if ledgerType ~= 'none' and ledgerType ~= 'hash' then
    return redis.error_reply('history ledger must be a hash')
end

if operation == 'clear' then
    redis.call('HSET', ledger, closedField, '1')
    redis.call('DEL', messages)
    return accepted
end

if redis.call('HGET', ledger, closedField) then
    if operation == 'read' then return {} end
    return closed
end

local messageType = redis.call('TYPE', messages).ok
if messageType ~= 'none' and messageType ~= 'list' then
    return redis.error_reply('history messages must be a list')
end

if operation == 'read' then
    return redis.call('LRANGE', messages, 0, -1)
end
if operation ~= 'append' then
    return redis.error_reply('unsupported history operation')
end

local receiptField = receiptPrefix .. ARGV[2]
local digest = ARGV[3]
local existing = redis.call('HGET', ledger, receiptField)
if existing then
    if existing ~= digest then return conflict end
    return accepted
end
local ttl = tonumber(ARGV[5])
if not ttl or ttl < 0 then
    return redis.error_reply('invalid history message retention')
end

-- Lua isolation does not roll back a later ACL failure. Check every write
-- before publishing either the message or its durable receipt.
if not redis.acl_check_cmd('RPUSH', messages, ARGV[4])
    or not redis.acl_check_cmd('HSET', ledger, receiptField, digest)
    or (ttl > 0 and not redis.acl_check_cmd('PEXPIRE', messages, ttl)) then
    return redis.error_reply('NOPERM history append write permission denied')
end

redis.call('RPUSH', messages, ARGV[4])
redis.call('HSET', ledger, receiptField, digest)
if ttl > 0 then redis.call('PEXPIRE', messages, ttl) end
return accepted
