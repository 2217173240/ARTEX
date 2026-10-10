Option Explicit
Dim shell, files, executable, action
Set shell = CreateObject("WScript.Shell")
Set files = CreateObject("Scripting.FileSystemObject")
executable = files.BuildPath(files.GetParentFolderName(WScript.ScriptFullName), "artex.exe")
action = ""
If WScript.Arguments.Count > 0 Then
    Select Case WScript.Arguments(0)
        Case "control", "stop"
            action = " " & WScript.Arguments(0)
        Case Else
            WScript.Quit 2
    End Select
End If
shell.Run Chr(34) & executable & Chr(34) & " launch" & action, 0, False
