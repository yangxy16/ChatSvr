var socket = null;
var bconnected = false
var timerHeartBeat = null;

$(function() {
    $("#localid").val(getLocalID())
    setState(false);
})

function getTime() {
    var date = new Date();
    return date.toLocaleString();
}

function scroll2End() {
    $("#contentScroll")[0].scrollTop = $("#contentScroll")[0].scrollHeight;
}

function getLocalID() {
    
    var chars = ['0','1','2','3','4','5','6','7','8','9'];
    var res = "";
    for(var i = 0; i < 5; i++) {
        var id = Math.ceil(Math.random()*9);
        res += chars[id];
    }
    return res;
}

function setState(bConnect) {
    
    if (bConnect) {
        $("#listenBtn").attr("disabled", true);
        $("#stopBtn").attr("disabled", false);
        $("#sendBtn").attr("disabled", false);
        
        $("#remoteid").attr("disabled", false);
        $("#serverip").attr("disabled", true);
        $("#localid").attr("disabled", true);
        
        bconnected = true
    } else {
        $("#listenBtn").attr("disabled", false);
        $("#stopBtn").attr("disabled", true);
        $("#sendBtn").attr("disabled", true);
        
        $("#remoteid").attr("disabled", false); 
        $("#serverip").attr("disabled", false);
        $("#localid").attr("disabled", false);
        
        bconnected = false;
    }
}

function sendmsg() {

    var rid = $("#remoteid").val();
    var lid = $("#localid").val();
    var text = encodeScript($("#msg").val());
    
    if (lid.length < 1) {
        alert( "请输入对方聊天ID！！！" );
        return;
    }
    
    
    if (rid == lid) {
        alert( "请不要给自己发消息！！！" );
        return;
    }
    
    if (text.length < 1) {
        alert( "请不要发送空消息！！！" );
        return;
    }
    
    var msg = {
        "msgType" : "USERMSG",
        "msgRemote" : rid,
        "msgBody" : text
    };
    msg = JSON.stringify(msg);
    socket.send(msg);
    $("#content").append("<kbd>" + getTime() + " 发送消息[" + text + "]到用户[" + rid + "]</kbd></br>");
    scroll2End();
    $("#msg").val("");
    heartbeat();
}

function heartbeat() {
    if (timerHeartBeat != null) {
        window.clearInterval(timerHeartBeat)
        timerHeartBeat = null
    }
    timerHeartBeat = window.setInterval(function(){
        var msg = {
            "msgType" : "HEARTBEAT",
            "msgRemote" : "10000",
            "msgBody" : ""
        };
        msg = JSON.stringify(msg);
        socket.send(msg);
        $("#content").append("<kbd>" + getTime() + " 向服务器发送心跳包！</kbd></br>");
    },30000);
}

function listen() {
    
    $("#listenBtn").attr("disabled", true);
    $("#stopBtn").attr("disabled", true);
    $("#sendBtn").attr("disabled", true);
    $("#remoteid").attr("disabled", true); 
    $("#serverip").attr("disabled", true);
    $("#localid").attr("disabled", true);
    
    var sip = $("#serverip").val();
    var lid = $("#localid").val();
    
    if ( sip.length > 0 && lid.length > 0 ) {
    
        socket = new WebSocket("ws://" + sip);
        socket.onopen = function() {
            $("#content").append("<kbd>" + getTime() + " 连接服务器成功！</kbd></br>");
            scroll2End();
            
            var msg = {
                "msgType" : "LOGIN",
                "msgRemote" : "10000",
                "msgBody" : lid
            };
            msg = JSON.stringify(msg);
            socket.send(msg);
            setState(true);
            heartbeat();
            
        };

        socket.onmessage = function(evt) {
            var data = JSON.parse(evt.data);
            if (data.msgRemote == "10000") {
                if (data.msgType == "LOGIN") {
                    if (data.msgBody == "OK") {
                        $("#content").append("<kbd>" + getTime() + " 登录成功！</kbd></br>");
                    } else if (data.msgBody == "RECONNECT") {
                        $("#content").append("<kbd>" + getTime() + " 你已经在其他地方登录了，不要重复登录！</kbd></br>");
                    }
                } else if (data.msgType == "HEARTBEAT") {
                    $("#content").append("<kbd>" + getTime() + " 接收到服务器心跳包！</kbd></br>");
                }
            } else {
                if (data.msgType == "USERMSG") {
                    $("#content").append("<kbd>" + getTime() + " 接收到来自[" + data.msgRemote + "]的消息[" + data.msgBody + "]</kbd></br>");
                } else if (data.msgType == "REMOTEOFFLINE") {
                    $("#content").append("<kbd>" + getTime() + " [" + data.msgRemote + "]当前不在线</kbd></br>");
                }
            }
            scroll2End();
        };

        socket.onclose = function(evt) {
            if(bconnected){
                $("#content").append("<kbd>" + getTime() + " 连接关闭！" + "</kbd></br>");
                scroll2End();
            }
            setState(false)
            if (timerHeartBeat){
                window.clearInterval(timerHeartBeat)
                timerHeartBeat = null
            }
        }

        socket.onerror = function(evt) {
            if(bconnected){
                $("#content").append("<kbd>" + getTime() + " 连接关闭！" + "</kbd></br>");
                scroll2End();
            } else {
                $("#content").append("<kbd>" + getTime() + " 无法连接到服务器！" + "</kbd></br>");
                scroll2End();
            }
            setState(false);
            if (timerHeartBeat){
                window.clearInterval(timerHeartBeat)
                timerHeartBeat = null
            }
        }
    } else {
        setState(false);
        alert("请正确输入服务器信息（例如：192.168.1.10:8000）和本机用户ID（例如：123456）");
    }
}

function stop() {
    if ( bconnected && socket != null ) {
        socket.close();
    }
}

function encodeScript(data) {
    if(null == data || "" == data) {
        return "";
    }
    return data.replace("<", "&lt;").replace(">", "&gt;");
}

document.onkeydown = function(event){
    var e = event || window.event || arguments.callee.caller.arguments[0];
    if(e && e.keyCode == 13) {
        sendmsg();
    }
}; 